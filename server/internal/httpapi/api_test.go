package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/johnlrt/deliciouscow/server/internal/auth"
	"github.com/johnlrt/deliciouscow/server/internal/httpapi"
	"github.com/johnlrt/deliciouscow/server/internal/store"
	"github.com/johnlrt/deliciouscow/server/internal/tokenizer"
	"github.com/johnlrt/deliciouscow/server/internal/types"
	"github.com/johnlrt/deliciouscow/server/internal/wallet"
	"github.com/johnlrt/deliciouscow/server/internal/ws"
)

func setupTestServer(t *testing.T) (*httptest.Server, *httpapi.Server) {
	t.Helper()
	st, err := store.NewMemoryStore("", nil)
	if err != nil {
		t.Fatal(err)
	}
	authCfg, err := auth.NewConfig("test-pepper", true)
	if err != nil {
		t.Fatal(err)
	}
	cat := tokenizer.DefaultCatalog()
	hub := ws.NewHub(st, authCfg, cat)
	srv := httpapi.NewServer(st, authCfg, tokenizer.SimpleEstimator{}, wallet.NewMockWallet("stagenet"), hub, cat, true, "")
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts, srv
}

func postJSON(t *testing.T, c *http.Client, url, csrf, bearer string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestEndToEndM1VerticalSlice(t *testing.T) {
	ts, _ := setupTestServer(t)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	// 1. Register
	resp := postJSON(t, client, ts.URL+"/api/auth/register", "", "", map[string]any{
		"username":               "alice",
		"password":               "correcthorsebatterystaple",
		"privacy_notice_version": "v1",
	})
	if resp.StatusCode != 201 {
		t.Fatalf("register status=%d", resp.StatusCode)
	}
	var reg types.RegisterResponse
	_ = json.NewDecoder(resp.Body).Decode(&reg)
	resp.Body.Close()
	if len(reg.RecoveryCodes) != 8 || reg.CSRFToken == "" {
		t.Fatalf("unexpected register response: %+v", reg)
	}

	// 2. Deposit via dev endpoint
	resp = postJSON(t, client, ts.URL+"/api/dev/deposit", reg.CSRFToken, "", map[string]any{
		"amount_piconero": "1000000000",
	})
	if resp.StatusCode != 201 {
		t.Fatalf("dev deposit status=%d", resp.StatusCode)
	}
	resp.Body.Close()

	// 3. Create API key
	resp = postJSON(t, client, ts.URL+"/api/keys", reg.CSRFToken, "", map[string]any{
		"name": "test-key",
	})
	if resp.StatusCode != 201 {
		t.Fatalf("create key status=%d", resp.StatusCode)
	}
	var keyResp struct {
		Key    types.APIKey `json:"key"`
		Secret string       `json:"secret"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&keyResp)
	resp.Body.Close()
	if !strings.HasPrefix(keyResp.Secret, "cvk_") {
		t.Fatalf("expected cvk_ secret, got %q", keyResp.Secret)
	}

	// 4. Node enrollment + link + poll
	resp = postJSON(t, &http.Client{}, ts.URL+"/api/node/enrollments", "", "", map[string]any{
		"hwid_hash":    strings.Repeat("a", 64),
		"node_version": "0.1.0",
		"device_name":  "gpu-box",
	})
	if resp.StatusCode != 201 {
		t.Fatalf("enrollment status=%d", resp.StatusCode)
	}
	var enr struct {
		UserCode   string `json:"user_code"`
		DeviceCode string `json:"device_code"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&enr)
	resp.Body.Close()

	// Poll before link -> 428
	resp = postJSON(t, &http.Client{}, ts.URL+"/api/node/enrollments/token", "", "", map[string]any{
		"device_code": enr.DeviceCode,
	})
	if resp.StatusCode != 428 {
		t.Fatalf("expected 428 pending, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// Link
	resp = postJSON(t, client, ts.URL+"/api/nodes/link", reg.CSRFToken, "", map[string]any{
		"user_code": enr.UserCode,
		"name":      "my-node",
	})
	if resp.StatusCode != 201 {
		t.Fatalf("link node status=%d", resp.StatusCode)
	}
	resp.Body.Close()

	// Poll after link -> 200
	resp = postJSON(t, &http.Client{}, ts.URL+"/api/node/enrollments/token", "", "", map[string]any{
		"device_code": enr.DeviceCode,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("poll token status=%d", resp.StatusCode)
	}
	var tokResp struct {
		NodeID     string `json:"node_id"`
		NodeSecret string `json:"node_secret"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&tokResp)
	resp.Body.Close()

	// 5. Node challenge/auth -> JWT -> WS
	resp = postJSON(t, &http.Client{}, ts.URL+"/api/node/auth/challenge", "", "", map[string]any{
		"node_id": tokResp.NodeID,
	})
	var chal struct {
		Nonce string `json:"nonce"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&chal)
	resp.Body.Close()

	proof := auth.ComputeNodeProof(tokResp.NodeSecret, tokResp.NodeID, chal.Nonce)
	resp = postJSON(t, &http.Client{}, ts.URL+"/api/node/auth/token", "", "", map[string]any{
		"node_id": tokResp.NodeID,
		"nonce":   chal.Nonce,
		"proof":   proof,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("node auth token status=%d", resp.StatusCode)
	}
	var authTok struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&authTok)
	resp.Body.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/node/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wsConn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + authTok.AccessToken}},
	})
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer wsConn.Close(websocket.StatusNormalClosure, "done")

	// Send hello
	sendWS := func(seq uint32, sid *string, reqID *string, typ string, payload any) {
		pb, _ := json.Marshal(payload)
		env := ws.Envelope{
			V:         1,
			Type:      typ,
			MessageID: uuid.NewString(),
			RequestID: reqID,
			SessionID: sid,
			Seq:       seq,
			SentAt:    time.Now().UTC().Format(time.RFC3339),
			Payload:   pb,
		}
		b, _ := json.Marshal(env)
		if err := wsConn.Write(ctx, websocket.MessageText, b); err != nil {
			t.Fatal(err)
		}
	}
	sendWS(1, nil, nil, "hello", map[string]any{
		"node_id":           tokResp.NodeID,
		"node_version":      "0.1.0",
		"protocol_versions": []int{1},
		"hardware":          types.Hardware{CPUModel: "test", CPUCores: 4, RAMBytes: 8 << 30, OS: "linux", Arch: "amd64"},
		"hwid_hash":         strings.Repeat("a", 64),
	})

	_, rawWelcome, err := wsConn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var welcomeEnv ws.Envelope
	_ = json.Unmarshal(rawWelcome, &welcomeEnv)
	if welcomeEnv.Type != "welcome" || welcomeEnv.SessionID == nil {
		t.Fatalf("expected welcome, got %s", welcomeEnv.Type)
	}
	sid := welcomeEnv.SessionID

	// Node loop goroutine to answer inference.start
	doneNode := make(chan struct{})
	go func() {
		defer close(doneNode)
		var outSeq uint32 = 1
		for {
			_, raw, err := wsConn.Read(ctx)
			if err != nil {
				return
			}
			var msg ws.Envelope
			if err := json.Unmarshal(raw, &msg); err != nil {
				return
			}
			if msg.Type == "inference.start" {
				var p struct {
					AttemptID string `json:"attempt_id"`
					LeaseID   string `json:"lease_id"`
				}
				_ = json.Unmarshal(msg.Payload, &p)
				outSeq++
				sendWS(outSeq, sid, msg.RequestID, "inference.accepted", map[string]any{
					"attempt_id": p.AttemptID, "lease_id": p.LeaseID,
				})
				outSeq++
				sendWS(outSeq, sid, msg.RequestID, "inference.delta", map[string]any{
					"attempt_id": p.AttemptID, "lease_id": p.LeaseID, "chunk_index": 0, "text": "Hello ",
				})
				outSeq++
				sendWS(outSeq, sid, msg.RequestID, "inference.delta", map[string]any{
					"attempt_id": p.AttemptID, "lease_id": p.LeaseID, "chunk_index": 1, "text": "world!",
				})
				outSeq++
				sendWS(outSeq, sid, msg.RequestID, "inference.complete", map[string]any{
					"attempt_id": p.AttemptID, "lease_id": p.LeaseID, "last_chunk_index": 1, "finish_reason": "stop", "claimed_input_tokens": 2, "claimed_output_tokens": 3,
				})
			}
		}
	}()

	// 6. Call /v1/chat/completions (non-stream)
	resp = postJSON(t, &http.Client{}, ts.URL+"/v1/chat/completions", "", keyResp.Secret, map[string]any{
		"model":    "cordova-tiny-chat",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if resp.StatusCode != 200 {
		t.Fatalf("chat completions status=%d", resp.StatusCode)
	}
	var chatResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&chatResp)
	resp.Body.Close()
	if len(chatResp.Choices) != 1 || chatResp.Choices[0].Message.Content != "Hello world!" {
		t.Fatalf("unexpected chat response: %+v", chatResp)
	}

	// 7. Call /v1/chat/completions (stream=true)
	resp = postJSON(t, &http.Client{}, ts.URL+"/v1/chat/completions", "", keyResp.Secret, map[string]any{
		"model":    "cordova-tiny-chat",
		"stream":   true,
		"messages": []map[string]string{{"role": "user", "content": "hi again"}},
	})
	if resp.StatusCode != 200 {
		t.Fatalf("stream status=%d", resp.StatusCode)
	}
	var sseBuf bytes.Buffer
	_, _ = sseBuf.ReadFrom(resp.Body)
	resp.Body.Close()
	if !strings.Contains(sseBuf.String(), "data: [DONE]") || !strings.Contains(sseBuf.String(), "Hello ") {
		t.Fatalf("unexpected SSE output: %s", sseBuf.String())
	}

	// 8. Check balance & usage
	uResp, err := client.Get(ts.URL + "/api/usage")
	if err != nil || uResp.StatusCode != 200 {
		t.Fatalf("usage err=%v", err)
	}
	var uPage types.Page[types.UsageRow]
	_ = json.NewDecoder(uResp.Body).Decode(&uPage)
	uResp.Body.Close()
	if len(uPage.Data) != 2 {
		t.Fatalf("expected 2 usage rows, got %d", len(uPage.Data))
	}
	for _, row := range uPage.Data {
		if row.Billable || row.TokenizationVerified {
			t.Fatalf("expected unverified SimpleEstimator usage to be marked non-billable, got %+v", row)
		}
		if row.BillingNote == nil || *row.BillingNote != "non_billable_unverified_tokenization" {
			t.Fatalf("expected billing_note=non_billable_unverified_tokenization, got %v", row.BillingNote)
		}
		if row.ChargedPiconero == nil || *row.ChargedPiconero != 0 {
			t.Fatalf("expected charged_piconero=0 for non-billable usage, got %v", row.ChargedPiconero)
		}
	}
}

func TestNonBillableUsageZeroMonetaryPostings(t *testing.T) {
	ts, srv := setupTestServer(t)
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	// Register + deposit 5000 piconero + create API key with tight daily spend cap of 1 piconero
	resp := postJSON(t, client, ts.URL+"/api/auth/register", "", "", map[string]any{
		"username":               "nonbillable",
		"password":               "correcthorsebatterystaple",
		"privacy_notice_version": "v1",
	})
	var reg types.RegisterResponse
	_ = json.NewDecoder(resp.Body).Decode(&reg)
	resp.Body.Close()

	resp = postJSON(t, client, ts.URL+"/api/dev/deposit", reg.CSRFToken, "", map[string]any{
		"amount_piconero": "5000",
	})
	resp.Body.Close()

	resp = postJSON(t, client, ts.URL+"/api/keys", reg.CSRFToken, "", map[string]any{
		"name":                     "capped-key",
		"daily_spend_cap_piconero": "1",
	})
	var keyResp struct {
		Key    types.APIKey `json:"key"`
		Secret string       `json:"secret"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&keyResp)
	resp.Body.Close()

	// Enroll & connect a mock WS node that handles: success, error, and cancel
	resp = postJSON(t, &http.Client{}, ts.URL+"/api/node/enrollments", "", "", map[string]any{
		"hwid_hash": strings.Repeat("b", 64), "node_version": "0.1.0", "device_name": "nb-node",
	})
	var enr struct {
		UserCode   string `json:"user_code"`
		DeviceCode string `json:"device_code"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&enr)
	resp.Body.Close()
	postJSON(t, client, ts.URL+"/api/nodes/link", reg.CSRFToken, "", map[string]any{
		"user_code": enr.UserCode, "name": "nb-node",
	}).Body.Close()
	resp = postJSON(t, &http.Client{}, ts.URL+"/api/node/enrollments/token", "", "", map[string]any{
		"device_code": enr.DeviceCode,
	})
	var tokResp struct {
		NodeID     string `json:"node_id"`
		NodeSecret string `json:"node_secret"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&tokResp)
	resp.Body.Close()
	resp = postJSON(t, &http.Client{}, ts.URL+"/api/node/auth/challenge", "", "", map[string]any{"node_id": tokResp.NodeID})
	var chal struct{ Nonce string `json:"nonce"` }
	_ = json.NewDecoder(resp.Body).Decode(&chal)
	resp.Body.Close()
	proof := auth.ComputeNodeProof(tokResp.NodeSecret, tokResp.NodeID, chal.Nonce)
	resp = postJSON(t, &http.Client{}, ts.URL+"/api/node/auth/token", "", "", map[string]any{
		"node_id": tokResp.NodeID, "nonce": chal.Nonce, "proof": proof,
	})
	var authTok struct{ AccessToken string `json:"access_token"` }
	_ = json.NewDecoder(resp.Body).Decode(&authTok)
	resp.Body.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/node/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wsConn, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + authTok.AccessToken}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer wsConn.Close(websocket.StatusNormalClosure, "done")

	sendWS := func(seq uint32, sid, reqID *string, typ string, payload any, errObj *ws.EnvelopeError) {
		pb, _ := json.Marshal(payload)
		env := ws.Envelope{
			V: 1, Type: typ, MessageID: uuid.NewString(), RequestID: reqID,
			SessionID: sid, Seq: seq, SentAt: time.Now().UTC().Format(time.RFC3339),
			Payload: pb, Error: errObj,
		}
		b, _ := json.Marshal(env)
		_ = wsConn.Write(ctx, websocket.MessageText, b)
	}
	sendWS(1, nil, nil, "hello", map[string]any{
		"node_id": tokResp.NodeID, "node_version": "0.1.0", "protocol_versions": []int{1},
		"hardware": types.Hardware{CPUModel: "test", CPUCores: 2, RAMBytes: 4 << 30, OS: "linux", Arch: "amd64"},
		"hwid_hash": strings.Repeat("b", 64),
	}, nil)
	_, rawWelcome, err := wsConn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var welcomeEnv ws.Envelope
	_ = json.Unmarshal(rawWelcome, &welcomeEnv)
	sid := welcomeEnv.SessionID

	modeCh := make(chan string, 3)
	modeCh <- "success"
	modeCh <- "error"
	modeCh <- "cancel"
	cancelAck := make(chan struct{}, 1)

	go func() {
		var seq uint32 = 1
		for {
			_, raw, err := wsConn.Read(ctx)
			if err != nil {
				return
			}
			var msg ws.Envelope
			if json.Unmarshal(raw, &msg) != nil {
				return
			}
			if msg.Type == "inference.start" {
				var p struct {
					AttemptID            string `json:"attempt_id"`
					LeaseID              string `json:"lease_id"`
					TokenizationVerified bool   `json:"tokenization_verified"`
				}
				_ = json.Unmarshal(msg.Payload, &p)
				if p.TokenizationVerified {
					t.Errorf("expected tokenization_verified=false for SimpleEstimator")
				}
				mode := <-modeCh
				switch mode {
				case "success":
					seq++
					sendWS(seq, sid, msg.RequestID, "inference.accepted", map[string]any{"attempt_id": p.AttemptID, "lease_id": p.LeaseID}, nil)
					seq++
					sendWS(seq, sid, msg.RequestID, "inference.delta", map[string]any{"attempt_id": p.AttemptID, "lease_id": p.LeaseID, "chunk_index": 0, "text": "ok"}, nil)
					seq++
					sendWS(seq, sid, msg.RequestID, "inference.complete", map[string]any{"attempt_id": p.AttemptID, "lease_id": p.LeaseID, "last_chunk_index": 0, "finish_reason": "stop"}, nil)
				case "error":
					seq++
					sendWS(seq, sid, msg.RequestID, "error", map[string]any{"attempt_id": p.AttemptID, "lease_id": p.LeaseID}, &ws.EnvelopeError{Code: "node_failed", Message: "boom", Retryable: false})
				case "cancel":
					seq++
					sendWS(seq, sid, msg.RequestID, "inference.accepted", map[string]any{"attempt_id": p.AttemptID, "lease_id": p.LeaseID}, nil)
					seq++
					sendWS(seq, sid, msg.RequestID, "inference.delta", map[string]any{"attempt_id": p.AttemptID, "lease_id": p.LeaseID, "chunk_index": 0, "text": "partial"}, nil)
				}
			} else if msg.Type == "inference.cancel" {
				select {
				case cancelAck <- struct{}{}:
				default:
				}
			}
		}
	}()

	// Snapshot financial state BEFORE requests
	var beforeAvail, beforeRes, beforeEarn, beforeKeySpent types.Money
	var beforeLedgerLen int
	_ = srv.Store.WithReadLock(func(tx *store.Tx) error {
		acc := tx.Accounts[reg.AccountID]
		beforeAvail = acc.AvailablePiconero
		beforeRes = acc.ReservedPiconero
		beforeEarn = acc.EarningsPendingPiconero
		beforeKeySpent = tx.APIKeys[keyResp.Key.ID].DailySpentPiconero
		beforeLedgerLen = len(tx.Ledger)
		return nil
	})

	assertUnchanged := func(label string) {
		t.Helper()
		_ = srv.Store.WithReadLock(func(tx *store.Tx) error {
			acc := tx.Accounts[reg.AccountID]
			if acc.AvailablePiconero != beforeAvail || acc.ReservedPiconero != beforeRes || acc.EarningsPendingPiconero != beforeEarn {
				t.Fatalf("%s: balance mutated: avail=%d (want %d), res=%d (want %d), earn=%d (want %d)",
					label, acc.AvailablePiconero, beforeAvail, acc.ReservedPiconero, beforeRes, acc.EarningsPendingPiconero, beforeEarn)
			}
			if got := tx.APIKeys[keyResp.Key.ID].DailySpentPiconero; got != beforeKeySpent {
				t.Fatalf("%s: spend cap mutated: got %d want %d", label, got, beforeKeySpent)
			}
			if len(tx.Ledger) != beforeLedgerLen {
				t.Fatalf("%s: expected zero new ledger entries, got %d (was %d)", label, len(tx.Ledger), beforeLedgerLen)
			}
			return nil
		})
	}

	// 1. Successful non-billable request (must succeed despite daily_spend_cap_piconero=1!)
	r1 := postJSON(t, &http.Client{}, ts.URL+"/v1/chat/completions", "", keyResp.Secret, map[string]any{
		"model": "cordova-tiny-chat", "messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	if r1.StatusCode != 200 {
		t.Fatalf("expected 200 for non-billable success, got %d", r1.StatusCode)
	}
	r1.Body.Close()
	assertUnchanged("after success")

	// 2. Errored non-billable request
	r2 := postJSON(t, &http.Client{}, ts.URL+"/v1/chat/completions", "", keyResp.Secret, map[string]any{
		"model": "cordova-tiny-chat", "messages": []map[string]string{{"role": "user", "content": "err"}},
	})
	if r2.StatusCode != 503 {
		t.Fatalf("expected 503 for node error, got %d", r2.StatusCode)
	}
	r2.Body.Close()
	assertUnchanged("after error")

	// 3. Cancelled non-billable streaming request
	reqCtx, reqCancel := context.WithCancel(context.Background())
	bodyBytes, _ := json.Marshal(map[string]any{
		"model": "cordova-tiny-chat", "stream": true, "messages": []map[string]string{{"role": "user", "content": "cancel"}},
	})
	httpReq, _ := http.NewRequestWithContext(reqCtx, "POST", ts.URL+"/v1/chat/completions", bytes.NewReader(bodyBytes))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+keyResp.Secret)
	r3, err := (&http.Client{}).Do(httpReq)
	if err != nil {
		t.Fatalf("cancel stream start: %v", err)
	}
	buf := make([]byte, 256)
	_, _ = r3.Body.Read(buf)
	reqCancel()
	r3.Body.Close()
	select {
	case <-cancelAck:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for node cancel")
	}
	time.Sleep(50 * time.Millisecond)
	assertUnchanged("after cancel")
}
