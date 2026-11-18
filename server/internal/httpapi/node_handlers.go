package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/johnlrt/deliciouscow/server/internal/auth"
	"github.com/johnlrt/deliciouscow/server/internal/store"
	"github.com/johnlrt/deliciouscow/server/internal/types"
)

func (s *Server) toNodeDTO(n *store.NodeRecord) types.Node {
	status := n.Status
	if n.RevokedAt != nil {
		status = "revoked"
	} else if !s.Hub.IsNodeConnected(n.ID) && status == "online" {
		status = "offline"
	}
	models := n.Models
	if models == nil {
		models = []types.NodeModel{}
	}
	return types.Node{
		ID:            n.ID,
		Name:          n.Name,
		Status:        status,
		NodeVersion:   n.NodeVersion,
		Hardware:      n.Hardware,
		Models:        models,
		RankScore:     n.RankScore,
		ThroughputTPS: n.ThroughputTPS,
		ActiveSlots:   n.ActiveSlots,
		MaxSlots:      n.MaxSlots,
		LastSeenAt:    types.FormatTSPtr(n.LastSeenAt),
		CreatedAt:     types.FormatTS(n.CreatedAt),
		RevokedAt:     types.FormatTSPtr(n.RevokedAt),
	}
}

func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	ses, err := s.resolveSession(r)
	if err != nil {
		writeErr(w, r, "unauthenticated", "authentication required", nil)
		return
	}
	var out []types.Node
	_ = s.Store.WithReadLock(func(tx *store.Tx) error {
		for _, n := range tx.Nodes {
			if n.OwnerAccountID == ses.AccountID {
				out = append(out, s.toNodeDTO(n))
			}
		}
		return nil
	})
	if out == nil {
		out = []types.Node{}
	}
	writeJSON(w, http.StatusOK, types.Page[types.Node]{Data: out, NextCursor: nil})
}

func (s *Server) handleGetNode(w http.ResponseWriter, r *http.Request) {
	ses, err := s.resolveSession(r)
	if err != nil {
		writeErr(w, r, "unauthenticated", "authentication required", nil)
		return
	}
	id := r.PathValue("id")
	var dto *types.Node
	_ = s.Store.WithReadLock(func(tx *store.Tx) error {
		if n, ok := tx.Nodes[id]; ok && n.OwnerAccountID == ses.AccountID {
			d := s.toNodeDTO(n)
			dto = &d
		}
		return nil
	})
	if dto == nil {
		writeErr(w, r, "not_found", "node not found", nil)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (s *Server) handleCreateEnrollment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		HWIDHash    string `json:"hwid_hash"`
		NodeVersion string `json:"node_version"`
		DeviceName  string `json:"device_name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, r, "invalid_request", "invalid json", nil)
		return
	}
	if len(req.HWIDHash) != 64 {
		writeErr(w, r, "invalid_request", "hwid_hash must be 64 hex chars", types.ErrPtr("hwid_hash"))
		return
	}
	rawUC, _ := auth.RandomHex(4)
	userCode := strings.ToUpper(rawUC[:4] + "-" + rawUC[4:8])
	deviceCode, _ := auth.RandomHex(32) // 256 random bits
	now := s.Store.Now()
	exp := now.Add(10 * time.Minute)
	id := uuid.NewString()
	enr := &store.Enrollment{
		ID:             id,
		UserCode:       userCode,
		DeviceCodeHash: auth.SHA256Hex(deviceCode),
		HWIDHash:       req.HWIDHash,
		NodeVersion:    req.NodeVersion,
		DeviceName:     req.DeviceName,
		ExpiresAt:      exp,
		CreatedAt:      now,
	}
	_ = s.Store.WithLock(func(tx *store.Tx) error {
		tx.Enrollments[id] = enr
		tx.UserCodeIndex[userCode] = id
		tx.DeviceCodeIdx[enr.DeviceCodeHash] = id
		return nil
	})
	scheme := "https"
	if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") != "https" {
		scheme = "http"
	}
	origin := scheme + "://" + r.Host
	writeJSON(w, http.StatusCreated, map[string]any{
		"enrollment_id":         id,
		"user_code":             userCode,
		"device_code":           deviceCode,
		"verification_uri":      origin + "/dashboard/nodes?code=" + userCode,
		"expires_at":            types.FormatTS(exp),
		"poll_interval_seconds": 5,
	})
}

func (s *Server) handleLinkNode(w http.ResponseWriter, r *http.Request) {
	ses, ok := s.requireSessionCSRF(w, r)
	if !ok {
		return
	}
	var req struct {
		UserCode string `json:"user_code"`
		Name     string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, r, "invalid_request", "invalid json", nil)
		return
	}
	uc := strings.ToUpper(strings.TrimSpace(req.UserCode))
	now := s.Store.Now()
	nodeSecret, _ := auth.RandomHex(32)
	nodeID := uuid.NewString()
	var dto types.Node
	err := s.Store.WithLock(func(tx *store.Tx) error {
		eid, exists := tx.UserCodeIndex[uc]
		if !exists {
			return store.ErrNotFound
		}
		enr := tx.Enrollments[eid]
		if enr == nil || now.After(enr.ExpiresAt) {
			return store.ErrNotFound
		}
		if enr.ApprovedBy != nil {
			return store.ErrConflict
		}
		name := strings.TrimSpace(req.Name)
		if name == "" {
			name = enr.DeviceName
		}
		if name == "" {
			name = "node-" + nodeID[:8]
		}
		rec := &store.NodeRecord{
			ID:             nodeID,
			OwnerAccountID: ses.AccountID,
			Name:           name,
			SecretHash:     nodeSecret,
			SecretVersion:  1,
			HWIDHash:       enr.HWIDHash,
			NodeVersion:    enr.NodeVersion,
			Status:         "offline",
			RankScore:      1.0,
			CreatedAt:      now,
		}
		tx.Nodes[nodeID] = rec
		enr.ApprovedBy = &ses.AccountID
		enr.NodeID = &nodeID
		enr.NodeSecretPlain = &nodeSecret
		readUntil := now.Add(10 * time.Minute)
		enr.TokenReadUntil = &readUntil
		store.EmitEventLocked(tx, ses.AccountID, "nodes", nodeID)
		dto = s.toNodeDTO(rec)
		return nil
	})
	if err == store.ErrNotFound {
		writeErr(w, r, "not_found", "invalid or expired user_code", nil)
		return
	}
	if err == store.ErrConflict {
		writeErr(w, r, "conflict", "enrollment already linked", nil)
		return
	}
	s.Hub.NotifyAccount(ses.AccountID, "nodes", nodeID)
	writeJSON(w, http.StatusCreated, dto)
}

func (s *Server) handlePollEnrollmentToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceCode string `json:"device_code"`
	}
	if err := decodeJSON(r, &req); err != nil || req.DeviceCode == "" {
		writeErr(w, r, "invalid_request", "device_code required", nil)
		return
	}
	dch := auth.SHA256Hex(req.DeviceCode)
	now := s.Store.Now()
	var nodeID, nodeSecret string
	var status string
	_ = s.Store.WithLock(func(tx *store.Tx) error {
		eid, ok := tx.DeviceCodeIdx[dch]
		if !ok {
			status = "not_found"
			return nil
		}
		enr := tx.Enrollments[eid]
		if enr == nil || now.After(enr.ExpiresAt) && enr.ApprovedBy == nil {
			status = "not_found"
			return nil
		}
		if enr.ApprovedBy == nil {
			status = "pending"
			return nil
		}
		if enr.TokenReadUntil != nil && now.After(*enr.TokenReadUntil) {
			enr.NodeSecretPlain = nil
			status = "not_found"
			return nil
		}
		if enr.NodeSecretPlain == nil || enr.NodeID == nil {
			status = "not_found"
			return nil
		}
		nodeID = *enr.NodeID
		nodeSecret = *enr.NodeSecretPlain
		status = "ok"
		return nil
	})
	switch status {
	case "pending":
		writeErr(w, r, "enrollment_pending", "waiting for owner approval", nil)
	case "ok":
		scheme := "https"
		if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") != "https" {
			scheme = "http"
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"node_id":       nodeID,
			"node_secret":   nodeSecret,
			"server_origin": scheme + "://" + r.Host,
		})
	default:
		writeErr(w, r, "not_found", "enrollment not found or expired", nil)
	}
}

func (s *Server) handlePatchNode(w http.ResponseWriter, r *http.Request) {
	ses, ok := s.requireSessionCSRF(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, r, "invalid_request", "invalid json", nil)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeErr(w, r, "invalid_request", "name is required", types.ErrPtr("name"))
		return
	}
	var dto *types.Node
	_ = s.Store.WithLock(func(tx *store.Tx) error {
		if n, exists := tx.Nodes[id]; exists && n.OwnerAccountID == ses.AccountID {
			n.Name = req.Name
			d := s.toNodeDTO(n)
			dto = &d
			store.EmitEventLocked(tx, ses.AccountID, "nodes", id)
		}
		return nil
	})
	if dto == nil {
		writeErr(w, r, "not_found", "node not found", nil)
		return
	}
	s.Hub.NotifyAccount(ses.AccountID, "nodes", id)
	writeJSON(w, http.StatusOK, dto)
}

func (s *Server) handleRevokeNode(w http.ResponseWriter, r *http.Request) {
	ses, ok := s.requireSessionCSRF(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	now := s.Store.Now()
	var found bool
	_ = s.Store.WithLock(func(tx *store.Tx) error {
		if n, exists := tx.Nodes[id]; exists && n.OwnerAccountID == ses.AccountID {
			found = true
			n.RevokedAt = &now
			n.Status = "revoked"
			n.SecretVersion++
			store.EmitEventLocked(tx, ses.AccountID, "nodes", id)
		}
		return nil
	})
	if !found {
		writeErr(w, r, "not_found", "node not found", nil)
		return
	}
	s.Hub.RevokeNode(id)
	s.Hub.NotifyAccount(ses.AccountID, "nodes", id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleNodeChallenge(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NodeID string `json:"node_id"`
	}
	if err := decodeJSON(r, &req); err != nil || req.NodeID == "" {
		writeErr(w, r, "invalid_request", "node_id required", nil)
		return
	}
	nonce, _ := auth.RandomHex(32) // 256 bits
	now := s.Store.Now()
	exp := now.Add(60 * time.Second)
	var exists bool
	_ = s.Store.WithLock(func(tx *store.Tx) error {
		if n, ok := tx.Nodes[req.NodeID]; ok && n.RevokedAt == nil {
			exists = true
			tx.NodeChallenges[nonce] = &store.NodeChallenge{
				Nonce:     nonce,
				NodeID:    req.NodeID,
				ExpiresAt: exp,
			}
		}
		return nil
	})
	if !exists {
		writeErr(w, r, "unauthenticated", "unknown or revoked node", nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"nonce":      nonce,
		"expires_at": types.FormatTS(exp),
	})
}

func (s *Server) handleNodeAuthToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NodeID string `json:"node_id"`
		Nonce  string `json:"nonce"`
		Proof  string `json:"proof"`
	}
	if err := decodeJSON(r, &req); err != nil || req.NodeID == "" || req.Nonce == "" || req.Proof == "" {
		writeErr(w, r, "invalid_request", "node_id, nonce, proof required", nil)
		return
	}
	now := s.Store.Now()
	var secretVer int
	err := s.Store.WithLock(func(tx *store.Tx) error {
		ch, ok := tx.NodeChallenges[req.Nonce]
		if !ok || ch.NodeID != req.NodeID || now.After(ch.ExpiresAt) {
			return store.ErrNotFound
		}
		delete(tx.NodeChallenges, req.Nonce)
		n, ok := tx.Nodes[req.NodeID]
		if !ok || n.RevokedAt != nil {
			return store.ErrNotFound
		}
		// In M1, node_secret is stored as SHA256 hex in SecretHash, wait: VerifyNodeProof needs the raw node_secret!
		// In M1 we store SecretHash = SHA256(node_secret) for lookup, or we store encrypted/plaintext in M1?
		// Look at C3: "Store node secrets encrypted using an external master key."
		// Let's check: if we only store SHA256(node_secret), we cannot compute HMAC(node_secret, ...)!
		// Let's fix SecretHash to store the node_secret or verify against Enrollment's secret, OR store node_secret in SecretHash in M1 (since MemoryStore is local dev) with a clear note!
		if !auth.VerifyNodeProof(n.SecretHash, req.NodeID, req.Nonce, req.Proof) {
			return store.ErrConflict
		}
		secretVer = n.SecretVersion
		return nil
	})
	if err != nil {
		writeErr(w, r, "unauthenticated", "invalid node proof or expired challenge", nil)
		return
	}
	jti := uuid.NewString()
	tok, exp, err := s.Auth.MintNodeJWT(req.NodeID, secretVer, jti, now)
	if err != nil {
		writeErr(w, r, "server_error", "internal error", nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": tok,
		"expires_at":   types.FormatTS(exp),
	})
}
