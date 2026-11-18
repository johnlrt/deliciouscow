package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/johnlrt/deliciouscow/server/internal/ledger"
	"github.com/johnlrt/deliciouscow/server/internal/store"
	"github.com/johnlrt/deliciouscow/server/internal/tokenizer"
	"github.com/johnlrt/deliciouscow/server/internal/types"
	"github.com/johnlrt/deliciouscow/server/internal/ws"
)

func (s *Server) resolveAPIKey(r *http.Request) (*store.APIKeyRecord, error) {
	ah := r.Header.Get("Authorization")
	if !strings.HasPrefix(ah, "Bearer cvk_") {
		return nil, store.ErrNotFound
	}
	secret := strings.TrimPrefix(ah, "Bearer ")
	verifier := s.Auth.HashWithPepper(secret)
	now := s.Store.Now()
	var out *store.APIKeyRecord
	err := s.Store.WithLock(func(tx *store.Tx) error {
		id, ok := tx.KeyHashIndex[verifier]
		if !ok {
			return store.ErrNotFound
		}
		k := tx.APIKeys[id]
		if k == nil || k.RevokedAt != nil {
			return store.ErrNotFound
		}
		k.LastUsedAt = &now
		cp := *k
		out = &cp
		return nil
	})
	return out, err
}

func (s *Server) findCatalogModel(id string) (types.Model, bool) {
	for _, m := range s.Catalog {
		if m.ID == id {
			m.Availability = s.Hub.ModelAvailability(m.ID)
			return m, true
		}
	}
	return types.Model{}, false
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	models := make([]types.Model, len(s.Catalog))
	for i, m := range s.Catalog {
		m.Availability = s.Hub.ModelAvailability(m.ID)
		models[i] = m
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   models,
	})
}

type chatCompletionReq struct {
	Model         string                  `json:"model"`
	Messages      []tokenizer.ChatMessage `json:"messages"`
	MaxTokens     *int                    `json:"max_tokens"`
	Temperature   *float64                `json:"temperature"`
	TopP          *float64                `json:"top_p"`
	Stop          []string                `json:"stop"`
	Seed          *int                    `json:"seed"`
	Stream        bool                    `json:"stream"`
	StreamOptions *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
	Tools     any `json:"tools"`
	Functions any `json:"functions"`
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	key, err := s.resolveAPIKey(r)
	if err != nil {
		writeErr(w, r, "unauthenticated", "invalid or revoked api key", nil)
		return
	}
	hasChat := false
	for _, p := range key.Permissions {
		if p == "chat:write" {
			hasChat = true
			break
		}
	}
	if !hasChat {
		writeErr(w, r, "forbidden", "api key lacks chat:write permission", nil)
		return
	}

	rawBody, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		writeErr(w, r, "invalid_request", "failed to read body", nil)
		return
	}
	var req chatCompletionReq
	if err := json.Unmarshal(rawBody, &req); err != nil {
		writeErr(w, r, "invalid_request", "invalid json", nil)
		return
	}
	if req.Tools != nil || req.Functions != nil {
		writeErr(w, r, "unsupported_parameter", "tools and functions are not supported in v1", types.ErrPtr("tools"))
		return
	}
	model, ok := s.findCatalogModel(req.Model)
	if !ok {
		writeErr(w, r, "model_not_found", "unknown model", types.ErrPtr("model"))
		return
	}
	if len(key.ModelIDs) > 0 {
		allowed := false
		for _, mid := range key.ModelIDs {
			if mid == model.ID {
				allowed = true
				break
			}
		}
		if !allowed {
			writeErr(w, r, "forbidden", "api key not permitted for this model", types.ErrPtr("model"))
			return
		}
	}
	if len(req.Messages) == 0 {
		writeErr(w, r, "invalid_request", "messages must be non-empty", types.ErrPtr("messages"))
		return
	}
	maxTok := 512
	if req.MaxTokens != nil {
		if *req.MaxTokens < 1 || *req.MaxTokens > model.MaxOutputTokens {
			writeErr(w, r, "invalid_request", "max_tokens out of range", types.ErrPtr("max_tokens"))
			return
		}
		maxTok = *req.MaxTokens
	}
	temp := 0.7
	if req.Temperature != nil {
		temp = *req.Temperature
	}
	topP := 1.0
	if req.TopP != nil {
		topP = *req.TopP
	}

	// Idempotency-Key check
	idemKey := r.Header.Get("Idempotency-Key")
	requestID := uuid.NewString()
	now := s.Store.Now()
	if idemKey != "" {
		scopeKey := key.AccountID + ":" + key.ID + ":" + idemKey
		bodyDigest := s.Auth.HashWithPepper(string(rawBody))
		var conflictCode string
		_ = s.Store.WithLock(func(tx *store.Tx) error {
			if existing, exists := tx.Idempotency[scopeKey]; exists {
				if existing.BodyDigest != bodyDigest {
					conflictCode = "invalid_request"
					return nil
				}
				if !existing.Terminal {
					conflictCode = "conflict"
					return nil
				}
				conflictCode = "replay_unavailable"
				return nil
			}
			tx.Idempotency[scopeKey] = &store.IdempotencyRecord{
				ScopeKey:   scopeKey,
				BodyDigest: bodyDigest,
				RequestID:  requestID,
				Terminal:   false,
				CreatedAt:  now,
			}
			return nil
		})
		if conflictCode != "" {
			writeErr(w, r, conflictCode, "idempotency key already used", nil)
			return
		}
		defer func() {
			_ = s.Store.WithLock(func(tx *store.Tx) error {
				if rec, exists := tx.Idempotency[scopeKey]; exists {
					rec.Terminal = true
				}
				return nil
			})
		}()
	}

	inputTokens, inputTokenIDs, err := s.Tok.CountInput(model.ID, req.Messages)
	if err != nil {
		writeErr(w, r, "tokenizer_unavailable", "failed to tokenize input", nil)
		return
	}
	if inputTokens+maxTok > model.ContextLength {
		writeErr(w, r, "invalid_request", "prompt + max_tokens exceeds model context_length", types.ErrPtr("max_tokens"))
		return
	}

	candidates := s.Hub.CandidateNodesForModel(model.ID)
	if len(candidates) == 0 {
		w.Header().Set("Retry-After", "2")
		writeErr(w, r, "no_capacity", "no nodes available for model", nil)
		return
	}

	_, reservation, err := ledger.ComputeReservation(inputTokens, maxTok, model.InputPricePer1MPiconero, model.OutputPricePer1MPiconero)
	if err != nil {
		writeErr(w, r, "invalid_request", "cost calculation overflow", nil)
		return
	}

	if s.Metrics != nil {
		s.Metrics.RequestsTotal.Add(1)
	}

	// Create usage row
	_ = s.Store.WithLock(func(tx *store.Tx) error {
		tx.Usage = append(tx.Usage, store.UsageRecord{
			RequestID:   requestID,
			AccountID:   key.AccountID,
			APIKeyID:    key.ID,
			ModelID:     model.ID,
			Status:      "accepted",
			InputTokens: inputTokens,
			CreatedAt:   now,
		})
		return nil
	})

	startTime := time.Now()
	var finalText strings.Builder
	var finishReason = "stop"
	var settled bool
	var lastErrCode = "node_failed"
	var lastErrMsg = "node failed"
	var headersSent bool

	// Try up to 2 nodes if failure happens before first content
	tried := make(map[string]bool)
	for attemptNum := 1; attemptNum <= 2; attemptNum++ {
		candidates = s.Hub.CandidateNodesForModel(model.ID)
		var chosenNode string
		for _, nid := range candidates {
			if !tried[nid] {
				chosenNode = nid
				break
			}
		}
		if chosenNode == "" {
			if attemptNum == 1 {
				lastErrCode = "no_capacity"
				lastErrMsg = "no capacity available"
			}
			break
		}
		tried[chosenNode] = true

		attemptID := uuid.NewString()
		leaseID := uuid.NewString()
		reserveOp := fmt.Sprintf("reserve:%s:%d", requestID, attemptNum)
		err = s.Store.WithLock(func(tx *store.Tx) error {
			_, e := ledger.Reserve(tx, reserveOp, key.AccountID, key.ID, requestID, attemptID, reservation, s.Store.Now())
			return e
		})
		if err != nil {
			if errors.Is(err, ledger.ErrInsufficientFunds) {
				writeErr(w, r, "insufficient_funds", "insufficient available balance", nil)
				s.markUsageTerminal(requestID, "refunded", 0, 0, startTime)
				return
			}
			if errors.Is(err, ledger.ErrSpendCapExceeded) {
				writeErr(w, r, "spend_cap_exceeded", "api key daily spend cap exceeded", nil)
				s.markUsageTerminal(requestID, "refunded", 0, 0, startTime)
				return
			}
			writeErr(w, r, "storage_unavailable", "reservation failed", nil)
			return
		}

		var ownerAccountID string
		_ = s.Store.WithReadLock(func(tx *store.Tx) error {
			if n, ok := tx.Nodes[chosenNode]; ok {
				ownerAccountID = n.OwnerAccountID
			}
			return nil
		})

		evCh, cancelNode, err := s.Hub.StartAttempt(r.Context(), chosenNode, ws.InferenceStartReq{
			RequestID:      requestID,
			AttemptID:      attemptID,
			LeaseID:        leaseID,
			ModelID:        model.ID,
			InputTokenIDs:  inputTokenIDs,
			InputTokens:    inputTokens,
			MaxTokens:      maxTok,
			Temperature:    temp,
			TopP:           topP,
			Stop:           req.Stop,
			Seed:           req.Seed,
			DeadlineAt:     s.Store.Now().Add(120 * time.Second),
		})
		if err != nil {
			_ = s.Store.WithLock(func(tx *store.Tx) error {
				_, e := ledger.Refund(tx, fmt.Sprintf("refund:%s:%d", requestID, attemptNum), key.AccountID, requestID, attemptID, reservation, s.Store.Now())
				return e
			})
			lastErrCode = "node_failed"
			lastErrMsg = "failed to dispatch to node"
			continue
		}

		// Wait for inference events
		attemptFailedBeforeContent := false
		clientCancelled := false
		var flusher http.Flusher
		if req.Stream {
			flusher, _ = w.(http.Flusher)
		}

		hbTicker := time.NewTicker(15 * time.Second)
	loop:
		for {
			select {
			case <-r.Context().Done():
				clientCancelled = true
				cancelNode("client_cancelled")
				break loop
			case <-hbTicker.C:
				if req.Stream && headersSent {
					_, _ = io.WriteString(w, ": heartbeat\n\n")
					if flusher != nil {
						flusher.Flush()
					}
				}
			case ev, ok := <-evCh:
				if !ok {
					if !settled && finalText.Len() == 0 {
						attemptFailedBeforeContent = true
					}
					break loop
				}
				switch ev.Kind {
				case "accepted":
					if req.Stream && !headersSent {
						w.Header().Set("Content-Type", "text/event-stream")
						w.Header().Set("Cache-Control", "no-cache")
						w.WriteHeader(http.StatusOK)
						headersSent = true
						writeSSEChunk(w, requestID, model.ID, map[string]any{"role": "assistant"}, nil, nil)
						if flusher != nil {
							flusher.Flush()
						}
					}
				case "delta":
					if req.Stream && !headersSent {
						w.Header().Set("Content-Type", "text/event-stream")
						w.Header().Set("Cache-Control", "no-cache")
						w.WriteHeader(http.StatusOK)
						headersSent = true
						writeSSEChunk(w, requestID, model.ID, map[string]any{"role": "assistant"}, nil, nil)
					}
					finalText.WriteString(ev.Text)
					if req.Stream {
						writeSSEChunk(w, requestID, model.ID, map[string]any{"content": ev.Text}, nil, nil)
						if flusher != nil {
							flusher.Flush()
						}
					}
				case "complete":
					if ev.FinishReason != "" {
						finishReason = ev.FinishReason
					}
					outTok, _ := s.Tok.CountOutput(model.ID, finalText.String())
					if outTok > maxTok {
						outTok = maxTok
					}
					actualCost, ownerCredit, _, _ := ledger.ComputeSettlement(inputTokens, outTok, model.InputPricePer1MPiconero, model.OutputPricePer1MPiconero, model.OwnerShareBPS)
					if actualCost > reservation {
						actualCost = reservation
					}
					settleErr := s.Store.WithLock(func(tx *store.Tx) error {
						_, e := ledger.Settle(tx, "settle:"+requestID, key.AccountID, ownerAccountID, key.ID, requestID, attemptID, reservation, actualCost, ownerCredit, s.Store.Now())
						return e
					})
					if settleErr != nil {
						if req.Stream && headersSent {
							writeSSEError(w, requestID, "storage_unavailable", "settlement failed")
							if flusher != nil {
								flusher.Flush()
							}
						} else {
							writeErr(w, r, "storage_unavailable", "settlement failed", nil)
						}
						s.markUsageTerminal(requestID, "recovering", outTok, actualCost, startTime)
						hbTicker.Stop()
						return
					}
					settled = true
					s.markUsageTerminal(requestID, "settled", outTok, actualCost, startTime)
					s.Hub.NotifyAccount(key.AccountID, "usage", requestID)
					s.Hub.NotifyAccount(key.AccountID, "billing", key.AccountID)
					if ownerAccountID != "" && ownerAccountID != key.AccountID {
						s.Hub.NotifyAccount(ownerAccountID, "billing", ownerAccountID)
					}

					if req.Stream {
						if !headersSent {
							w.Header().Set("Content-Type", "text/event-stream")
							w.Header().Set("Cache-Control", "no-cache")
							w.WriteHeader(http.StatusOK)
							headersSent = true
						}
						fr := finishReason
						writeSSEChunk(w, requestID, model.ID, map[string]any{}, &fr, nil)
						if req.StreamOptions != nil && req.StreamOptions.IncludeUsage {
							writeSSEUsageChunk(w, requestID, model.ID, inputTokens, outTok)
						}
						_, _ = io.WriteString(w, "data: [DONE]\n\n")
						if flusher != nil {
							flusher.Flush()
						}
					} else {
						writeJSON(w, http.StatusOK, map[string]any{
							"id":      "chatcmpl-" + requestID,
							"object":  "chat.completion",
							"created": now.Unix(),
							"model":   model.ID,
							"choices": []map[string]any{{
								"index": 0,
								"message": map[string]any{
									"role":    "assistant",
									"content": finalText.String(),
								},
								"finish_reason": finishReason,
							}},
							"usage": map[string]any{
								"prompt_tokens":     inputTokens,
								"completion_tokens": outTok,
								"total_tokens":      inputTokens + outTok,
							},
						})
					}
					hbTicker.Stop()
					return
				case "error":
					lastErrCode = ev.ErrCode
					lastErrMsg = ev.ErrMsg
					if finalText.Len() == 0 {
						attemptFailedBeforeContent = true
					}
					break loop
				}
			}
		}
		hbTicker.Stop()

		if clientCancelled {
			outTok, _ := s.Tok.CountOutput(model.ID, finalText.String())
			if outTok == 0 {
				_ = s.Store.WithLock(func(tx *store.Tx) error {
					_, e := ledger.Refund(tx, fmt.Sprintf("refund:%s:%d", requestID, attemptNum), key.AccountID, requestID, attemptID, reservation, s.Store.Now())
					return e
				})
				s.markUsageTerminal(requestID, "cancelled", 0, 0, startTime)
			} else {
				actualCost, ownerCredit, _, _ := ledger.ComputeSettlement(inputTokens, outTok, model.InputPricePer1MPiconero, model.OutputPricePer1MPiconero, model.OwnerShareBPS)
				_ = s.Store.WithLock(func(tx *store.Tx) error {
					_, e := ledger.Settle(tx, "settle:"+requestID, key.AccountID, ownerAccountID, key.ID, requestID, attemptID, reservation, actualCost, ownerCredit, s.Store.Now())
					return e
				})
				s.markUsageTerminal(requestID, "cancelled", outTok, actualCost, startTime)
			}
			return
		}

		// Refund failed attempt
		_ = s.Store.WithLock(func(tx *store.Tx) error {
			_, e := ledger.Refund(tx, fmt.Sprintf("refund:%s:%d", requestID, attemptNum), key.AccountID, requestID, attemptID, reservation, s.Store.Now())
			return e
		})
		if !attemptFailedBeforeContent || headersSent {
			break
		}
	}

	s.markUsageTerminal(requestID, "refunded", 0, 0, startTime)
	if req.Stream && headersSent {
		writeSSEError(w, requestID, lastErrCode, lastErrMsg)
		return
	}
	if lastErrCode == "no_capacity" {
		w.Header().Set("Retry-After", "2")
	}
	writeErr(w, r, lastErrCode, lastErrMsg, nil)
}

func (s *Server) markUsageTerminal(requestID, status string, outTokens int, charged types.Money, start time.Time) {
	if s.Metrics != nil {
		if status == "settled" {
			s.Metrics.RequestsSettled.Add(1)
		} else {
			s.Metrics.RequestsFailed.Add(1)
		}
	}
	now := s.Store.Now()
	lat := time.Since(start).Milliseconds()
	_ = s.Store.WithLock(func(tx *store.Tx) error {
		for i := len(tx.Usage) - 1; i >= 0; i-- {
			if tx.Usage[i].RequestID == requestID {
				tx.Usage[i].Status = status
				tx.Usage[i].OutputTokens = outTokens
				if status != "recovering" {
					c := charged
					tx.Usage[i].ChargedPiconero = &c
				}
				tx.Usage[i].LatencyMS = &lat
				tx.Usage[i].FinishedAt = &now
				break
			}
		}
		return nil
	})
}

func writeSSEChunk(w io.Writer, reqID, modelID string, delta map[string]any, finishReason *string, usage any) {
	chunk := map[string]any{
		"id":      "chatcmpl-" + reqID,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   modelID,
		"choices": []map[string]any{{
			"index":         0,
			"delta":         delta,
			"finish_reason": finishReason,
		}},
		"usage": usage,
	}
	b, _ := json.Marshal(chunk)
	_, _ = io.WriteString(w, "data: "+string(b)+"\n\n")
}

func writeSSEUsageChunk(w io.Writer, reqID, modelID string, inTok, outTok int) {
	chunk := map[string]any{
		"id":      "chatcmpl-" + reqID,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   modelID,
		"choices": []any{},
		"usage": map[string]any{
			"prompt_tokens":     inTok,
			"completion_tokens": outTok,
			"total_tokens":      inTok + outTok,
		},
	}
	b, _ := json.Marshal(chunk)
	_, _ = io.WriteString(w, "data: "+string(b)+"\n\n")
}

func writeSSEError(w io.Writer, reqID, code, msg string) {
	_, errType := types.SafeErrorCodeStatus(code)
	b, _ := json.Marshal(types.APIError{
		Error: types.APIErrorBody{
			Message: msg,
			Type:    errType,
			Code:    code,
		},
		RequestID: reqID,
	})
	_, _ = io.WriteString(w, "data: "+string(b)+"\n\n")
}
