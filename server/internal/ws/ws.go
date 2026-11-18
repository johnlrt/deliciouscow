package ws

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/johnlrt/deliciouscow/server/internal/auth"
	"github.com/johnlrt/deliciouscow/server/internal/store"
	"github.com/johnlrt/deliciouscow/server/internal/types"
)

const MaxFrameBytes = 1048576

type EnvelopeError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type Envelope struct {
	V         int             `json:"v"`
	Type      string          `json:"type"`
	MessageID string          `json:"message_id"`
	RequestID *string         `json:"request_id"`
	SessionID *string         `json:"session_id"`
	Seq       uint32          `json:"seq"`
	SentAt    string          `json:"sent_at"`
	Payload   json.RawMessage `json:"payload"`
	Error     *EnvelopeError  `json:"error"`
}

type InferenceEvent struct {
	Kind         string // "accepted", "delta", "complete", "error", "cancelled"
	ChunkIndex   int
	Text         string
	FinishReason string
	ErrCode      string
	ErrMsg       string
}

type InferenceStartReq struct {
	RequestID      string
	AttemptID      string
	LeaseID        string
	ModelID        string
	CatalogVersion string
	InputTokenIDs  []int
	InputTokens    int
	MaxTokens      int
	Temperature    float64
	TopP           float64
	Stop           []string
	Seed           *int
	DeadlineAt     time.Time
}

type activeAttempt struct {
	requestID string
	attemptID string
	leaseID   string
	ch        chan InferenceEvent
	closed    atomic.Bool
}

type nodeConn struct {
	nodeID    string
	ownerID   string
	sessionID string
	conn      *websocket.Conn
	sendMu    sync.Mutex
	outSeq    uint32
	inSeq     uint32
	attempts  sync.Map // attemptID -> *activeAttempt
	closed    chan struct{}
	closeOnce sync.Once
}

func (nc *nodeConn) close(code websocket.StatusCode, reason string) {
	nc.closeOnce.Do(func() {
		close(nc.closed)
		_ = nc.conn.Close(code, reason)
		nc.attempts.Range(func(k, v any) bool {
			att := v.(*activeAttempt)
			if att.closed.CompareAndSwap(false, true) {
				select {
				case att.ch <- InferenceEvent{Kind: "error", ErrCode: "node_failed", ErrMsg: "node disconnected"}:
				default:
				}
				close(att.ch)
			}
			return true
		})
	})
}

func (nc *nodeConn) sendEnvelope(ctx context.Context, msgType string, reqID *string, payload any, envErr *EnvelopeError) error {
	nc.sendMu.Lock()
	defer nc.sendMu.Unlock()
	nc.outSeq++
	pb, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	sid := nc.sessionID
	env := Envelope{
		V:         1,
		Type:      msgType,
		MessageID: uuid.NewString(),
		RequestID: reqID,
		SessionID: &sid,
		Seq:       nc.outSeq,
		SentAt:    time.Now().UTC().Format(time.RFC3339),
		Payload:   pb,
		Error:     envErr,
	}
	b, err := json.Marshal(env)
	if err != nil {
		return err
	}
	return nc.conn.Write(ctx, websocket.MessageText, b)
}

type liveClient struct {
	accountID string
	sessionID string
	conn      *websocket.Conn
	sendMu    sync.Mutex
	outSeq    uint32
	topics    map[string]bool
}

func (lc *liveClient) sendEnvelope(ctx context.Context, msgType string, reqID *string, payload any, envErr *EnvelopeError) error {
	lc.sendMu.Lock()
	defer lc.sendMu.Unlock()
	lc.outSeq++
	pb, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	sid := lc.sessionID
	env := Envelope{
		V:         1,
		Type:      msgType,
		MessageID: uuid.NewString(),
		RequestID: reqID,
		SessionID: &sid,
		Seq:       lc.outSeq,
		SentAt:    time.Now().UTC().Format(time.RFC3339),
		Payload:   pb,
		Error:     envErr,
	}
	b, err := json.Marshal(env)
	if err != nil {
		return err
	}
	return lc.conn.Write(ctx, websocket.MessageText, b)
}

type Hub struct {
	store       store.Store
	authCfg     *auth.Config
	catalog     []types.Model
	catalogVer  string
	nodesMu     sync.RWMutex
	nodes       map[string]*nodeConn
	liveMu      sync.RWMutex
	liveClients map[string]map[*liveClient]struct{} // accountID -> set
}

func NewHub(st store.Store, authCfg *auth.Config, catalog []types.Model) *Hub {
	return &Hub{
		store:       st,
		authCfg:     authCfg,
		catalog:     catalog,
		catalogVer:  "v1-m1",
		nodes:       make(map[string]*nodeConn),
		liveClients: make(map[string]map[*liveClient]struct{}),
	}
}

func (h *Hub) IsNodeConnected(nodeID string) bool {
	h.nodesMu.RLock()
	defer h.nodesMu.RUnlock()
	_, ok := h.nodes[nodeID]
	return ok
}

func (h *Hub) RevokeNode(nodeID string) {
	h.nodesMu.RLock()
	nc := h.nodes[nodeID]
	h.nodesMu.RUnlock()
	if nc != nil {
		_ = nc.sendEnvelope(context.Background(), "drain", nil, map[string]any{
			"deadline_at": time.Now().UTC().Add(2 * time.Second).Format(time.RFC3339),
			"reason":      "revoked",
		}, nil)
		nc.close(websocket.StatusCode(4001), "node revoked")
	}
}

func (h *Hub) CandidateNodesForModel(modelID string) []string {
	var candidates []string
	_ = h.store.WithReadLock(func(tx *store.Tx) error {
		h.nodesMu.RLock()
		defer h.nodesMu.RUnlock()
		for nodeID := range h.nodes {
			rec, ok := tx.Nodes[nodeID]
			if !ok || rec.RevokedAt != nil || rec.Status == "revoked" || rec.Status == "draining" {
				continue
			}
			// Check if node reported model loaded or has no model filter (mock/default)
			eligible := len(rec.Models) == 0
			for _, m := range rec.Models {
				if m.ModelID == modelID && m.State == "loaded" && m.ActiveSlots < m.MaxSlots {
					eligible = true
					break
				}
			}
			if eligible {
				candidates = append(candidates, nodeID)
			}
		}
		return nil
	})
	return candidates
}

func (h *Hub) ModelAvailability(modelID string) string {
	if len(h.CandidateNodesForModel(modelID)) > 0 {
		return "available"
	}
	return "unavailable"
}

func (h *Hub) StartAttempt(ctx context.Context, nodeID string, req InferenceStartReq) (<-chan InferenceEvent, func(reason string), error) {
	h.nodesMu.RLock()
	nc := h.nodes[nodeID]
	h.nodesMu.RUnlock()
	if nc == nil {
		return nil, nil, errors.New("node_offline")
	}
	att := &activeAttempt{
		requestID: req.RequestID,
		attemptID: req.AttemptID,
		leaseID:   req.LeaseID,
		ch:        make(chan InferenceEvent, 64),
	}
	nc.attempts.Store(req.AttemptID, att)

	reqID := req.RequestID
	payload := map[string]any{
		"attempt_id":      req.AttemptID,
		"lease_id":        req.LeaseID,
		"model_id":        req.ModelID,
		"catalog_version": h.catalogVer,
		"input_token_ids": req.InputTokenIDs,
		"input_tokens":    req.InputTokens,
		"max_tokens":      req.MaxTokens,
		"parameters": map[string]any{
			"temperature": req.Temperature,
			"top_p":       req.TopP,
			"stop":        req.Stop,
			"seed":        req.Seed,
		},
		"deadline_at": req.DeadlineAt.UTC().Format(time.RFC3339),
		"kind":        "user",
	}
	if err := nc.sendEnvelope(ctx, "inference.start", &reqID, payload, nil); err != nil {
		nc.attempts.Delete(req.AttemptID)
		return nil, nil, err
	}
	cancelFn := func(reason string) {
		_ = nc.sendEnvelope(context.Background(), "inference.cancel", &reqID, map[string]any{
			"attempt_id": req.AttemptID,
			"lease_id":   req.LeaseID,
			"reason":     reason,
		}, nil)
	}
	return att.ch, cancelFn, nil
}

func (h *Hub) NotifyAccount(accountID, topic, resourceID string) {
	var latest *store.LiveEvent
	_ = h.store.WithReadLock(func(tx *store.Tx) error {
		evs := tx.LiveEvents[accountID]
		if len(evs) > 0 {
			ev := evs[len(evs)-1]
			latest = &ev
		}
		return nil
	})
	if latest == nil {
		return
	}
	h.liveMu.RLock()
	clients := h.liveClients[accountID]
	list := make([]*liveClient, 0, len(clients))
	for c := range clients {
		list = append(list, c)
	}
	h.liveMu.RUnlock()

	for _, c := range list {
		if len(c.topics) == 0 || c.topics[latest.Topic] {
			_ = c.sendEnvelope(context.Background(), "live.event", nil, latest, nil)
		}
	}
}

func (h *Hub) ServeNodeWS(w http.ResponseWriter, r *http.Request) {
	bearer := ""
	if ah := r.Header.Get("Authorization"); len(ah) > 7 && ah[:7] == "Bearer " {
		bearer = ah[7:]
	}
	if bearer == "" {
		bearer = r.URL.Query().Get("token")
	}
	if bearer == "" {
		http.Error(w, "missing token", http.StatusUnauthorized)
		return
	}
	claims, err := h.authCfg.VerifyNodeJWT(bearer, h.store.Now())
	if err != nil {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	var ownerID string
	err = h.store.WithLock(func(tx *store.Tx) error {
		if _, used := tx.UsedJTIs[claims.JTI]; used {
			return errors.New("replayed jti")
		}
		rec, ok := tx.Nodes[claims.Sub]
		if !ok || rec.RevokedAt != nil || rec.SecretVersion != claims.SecretVersion {
			return errors.New("node revoked or version mismatch")
		}
		tx.UsedJTIs[claims.JTI] = h.store.Now()
		ownerID = rec.OwnerAccountID
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}
	conn.SetReadLimit(MaxFrameBytes)

	sessionID := uuid.NewString()
	nc := &nodeConn{
		nodeID:    claims.Sub,
		ownerID:   ownerID,
		sessionID: sessionID,
		conn:      conn,
		closed:    make(chan struct{}),
	}

	// Read hello within 5s
	helloCtx, cancelHello := context.WithTimeout(r.Context(), 5*time.Second)
	mt, data, err := conn.Read(helloCtx)
	cancelHello()
	if err != nil || mt != websocket.MessageText {
		nc.close(websocket.StatusPolicyViolation, "expected hello")
		return
	}
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil || env.V != 1 || env.Type != "hello" || env.Seq != 1 || env.SessionID != nil {
		nc.close(websocket.StatusProtocolError, "invalid hello envelope")
		return
	}
	var helloPayload struct {
		NodeID           string         `json:"node_id"`
		NodeVersion      string         `json:"node_version"`
		ProtocolVersions []int          `json:"protocol_versions"`
		Hardware         types.Hardware `json:"hardware"`
		HWIDHash         string         `json:"hwid_hash"`
	}
	if err := json.Unmarshal(env.Payload, &helloPayload); err != nil || helloPayload.NodeID != claims.Sub {
		nc.close(websocket.StatusPolicyViolation, "node_id mismatch")
		return
	}
	nc.inSeq = 1

	// Register node session, fencing any prior session
	h.nodesMu.Lock()
	old := h.nodes[nc.nodeID]
	h.nodes[nc.nodeID] = nc
	h.nodesMu.Unlock()
	if old != nil {
		old.close(websocket.StatusCode(4001), "superseded by new session")
	}

	now := h.store.Now()
	_ = h.store.WithLock(func(tx *store.Tx) error {
		if rec, ok := tx.Nodes[nc.nodeID]; ok {
			rec.Status = "online"
			rec.NodeVersion = helloPayload.NodeVersion
			rec.Hardware = helloPayload.Hardware
			rec.LastSeenAt = &now
			store.EmitEventLocked(tx, rec.OwnerAccountID, "nodes", rec.ID)
		}
		return nil
	})
	h.NotifyAccount(ownerID, "nodes", nc.nodeID)

	welcomePayload := map[string]any{
		"session_id":            sessionID,
		"heartbeat_interval_ms": 15000,
		"heartbeat_timeout_ms":  45000,
		"catalog_version":       h.catalogVer,
		"max_frame_bytes":       MaxFrameBytes,
		"connection_expires_at": now.Add(24 * time.Hour).Format(time.RFC3339),
	}
	if err := nc.sendEnvelope(r.Context(), "welcome", env.RequestID, welcomePayload, nil); err != nil {
		nc.close(websocket.StatusInternalError, "welcome failed")
		return
	}

	defer func() {
		h.nodesMu.Lock()
		if h.nodes[nc.nodeID] == nc {
			delete(h.nodes, nc.nodeID)
		}
		h.nodesMu.Unlock()
		nc.close(websocket.StatusNormalClosure, "bye")
		t := h.store.Now()
		_ = h.store.WithLock(func(tx *store.Tx) error {
			if rec, ok := tx.Nodes[nc.nodeID]; ok {
				if rec.RevokedAt == nil {
					rec.Status = "offline"
				}
				rec.LastSeenAt = &t
				store.EmitEventLocked(tx, rec.OwnerAccountID, "nodes", rec.ID)
			}
			return nil
		})
		h.NotifyAccount(ownerID, "nodes", nc.nodeID)
	}()

	for {
		readCtx, cancelRead := context.WithTimeout(r.Context(), 45*time.Second)
		mt, raw, err := conn.Read(readCtx)
		cancelRead()
		if err != nil {
			return
		}
		if mt != websocket.MessageText {
			nc.close(websocket.StatusPolicyViolation, "binary frames forbidden")
			return
		}
		var msg Envelope
		if err := json.Unmarshal(raw, &msg); err != nil || msg.V != 1 {
			nc.close(websocket.StatusProtocolError, "bad envelope")
			return
		}
		if msg.SessionID == nil || *msg.SessionID != sessionID {
			nc.close(websocket.StatusPolicyViolation, "stale session")
			return
		}
		if msg.Seq != nc.inSeq+1 {
			nc.close(websocket.StatusProtocolError, "invalid seq")
			return
		}
		nc.inSeq = msg.Seq

		h.handleNodeMessage(nc, &msg)
	}
}

func (h *Hub) handleNodeMessage(nc *nodeConn, msg *Envelope) {
	switch msg.Type {
	case "heartbeat":
		var p struct {
			ActiveLeases []string          `json:"active_leases"`
			Hardware     types.Hardware    `json:"hardware"`
			Models       []types.NodeModel `json:"models"`
		}
		_ = json.Unmarshal(msg.Payload, &p)
		now := h.store.Now()
		_ = h.store.WithLock(func(tx *store.Tx) error {
			if rec, ok := tx.Nodes[nc.nodeID]; ok {
				rec.LastSeenAt = &now
				rec.Hardware = p.Hardware
				rec.Models = p.Models
				maxS := 0
				actS := 0
				for _, m := range p.Models {
					maxS += m.MaxSlots
					actS += m.ActiveSlots
				}
				if maxS > 0 {
					rec.MaxSlots = maxS
					rec.ActiveSlots = actS
				}
			}
			return nil
		})
		_ = nc.sendEnvelope(context.Background(), "heartbeat.ack", msg.RequestID, map[string]any{
			"server_time":        now.Format(time.RFC3339),
			"session_expires_at": now.Add(24 * time.Hour).Format(time.RFC3339),
		}, nil)

	case "catalog.get":
		_ = nc.sendEnvelope(context.Background(), "catalog.snapshot", msg.RequestID, map[string]any{
			"version": h.catalogVer,
			"models":  h.catalog,
		}, nil)

	case "inference.accepted":
		var p struct {
			AttemptID string `json:"attempt_id"`
			LeaseID   string `json:"lease_id"`
		}
		if err := json.Unmarshal(msg.Payload, &p); err == nil {
			if v, ok := nc.attempts.Load(p.AttemptID); ok {
				att := v.(*activeAttempt)
				if !att.closed.Load() {
					att.ch <- InferenceEvent{Kind: "accepted"}
				}
			}
		}

	case "inference.delta":
		var p struct {
			AttemptID  string `json:"attempt_id"`
			LeaseID    string `json:"lease_id"`
			ChunkIndex int    `json:"chunk_index"`
			Text       string `json:"text"`
		}
		if err := json.Unmarshal(msg.Payload, &p); err == nil {
			if v, ok := nc.attempts.Load(p.AttemptID); ok {
				att := v.(*activeAttempt)
				if !att.closed.Load() {
					att.ch <- InferenceEvent{Kind: "delta", ChunkIndex: p.ChunkIndex, Text: p.Text}
				}
			}
		}

	case "inference.complete":
		var p struct {
			AttemptID    string `json:"attempt_id"`
			LeaseID      string `json:"lease_id"`
			FinishReason string `json:"finish_reason"`
		}
		if err := json.Unmarshal(msg.Payload, &p); err == nil {
			if v, ok := nc.attempts.LoadAndDelete(p.AttemptID); ok {
				att := v.(*activeAttempt)
				if att.closed.CompareAndSwap(false, true) {
					att.ch <- InferenceEvent{Kind: "complete", FinishReason: p.FinishReason}
					close(att.ch)
				}
			}
		}

	case "inference.cancelled":
		var p struct {
			AttemptID string `json:"attempt_id"`
		}
		if err := json.Unmarshal(msg.Payload, &p); err == nil {
			if v, ok := nc.attempts.LoadAndDelete(p.AttemptID); ok {
				att := v.(*activeAttempt)
				if att.closed.CompareAndSwap(false, true) {
					att.ch <- InferenceEvent{Kind: "cancelled"}
					close(att.ch)
				}
			}
		}

	case "error":
		var p struct {
			AttemptID *string `json:"attempt_id"`
		}
		_ = json.Unmarshal(msg.Payload, &p)
		if p.AttemptID != nil {
			if v, ok := nc.attempts.LoadAndDelete(*p.AttemptID); ok {
				att := v.(*activeAttempt)
				if att.closed.CompareAndSwap(false, true) {
					code, message := "node_failed", "node error"
					if msg.Error != nil {
						code = msg.Error.Code
						message = msg.Error.Message
					}
					att.ch <- InferenceEvent{Kind: "error", ErrCode: code, ErrMsg: message}
					close(att.ch)
				}
			}
		}

	case "goodbye":
		nc.close(websocket.StatusNormalClosure, "goodbye")
	}
}

// ServeLiveWS implements C4 Dashboard Live WebSocket on /api/live.
func (h *Hub) ServeLiveWS(w http.ResponseWriter, r *http.Request, resolveSession func(r *http.Request) (*store.Session, error)) {
	ses, err := resolveSession(r)
	if err != nil {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}
	conn.SetReadLimit(MaxFrameBytes)

	lc := &liveClient{
		accountID: ses.AccountID,
		sessionID: ses.ID,
		conn:      conn,
		topics:    make(map[string]bool),
	}

	h.liveMu.Lock()
	if h.liveClients[ses.AccountID] == nil {
		h.liveClients[ses.AccountID] = make(map[*liveClient]struct{})
	}
	h.liveClients[ses.AccountID][lc] = struct{}{}
	h.liveMu.Unlock()

	defer func() {
		h.liveMu.Lock()
		delete(h.liveClients[ses.AccountID], lc)
		h.liveMu.Unlock()
		_ = conn.Close(websocket.StatusNormalClosure, "closed")
	}()

	_ = lc.sendEnvelope(r.Context(), "live.ready", nil, map[string]any{
		"session_id":            ses.ID,
		"account_id":            ses.AccountID,
		"heartbeat_interval_ms": 15000,
	}, nil)

	for {
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		mt, raw, err := conn.Read(ctx)
		cancel()
		if err != nil || mt != websocket.MessageText {
			return
		}
		var msg Envelope
		if err := json.Unmarshal(raw, &msg); err != nil {
			return
		}
		switch msg.Type {
		case "live.ping":
			_ = lc.sendEnvelope(r.Context(), "live.pong", msg.RequestID, map[string]any{
				"time": time.Now().UTC().Format(time.RFC3339),
			}, nil)
		case "live.subscribe":
			var p struct {
				Topics      []string `json:"topics"`
				LastEventID *string  `json:"last_event_id"`
			}
			_ = json.Unmarshal(msg.Payload, &p)
			for _, t := range p.Topics {
				lc.topics[t] = true
			}
		}
	}
}
