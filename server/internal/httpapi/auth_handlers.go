package httpapi

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/johnlrt/deliciouscow/server/internal/auth"
	"github.com/johnlrt/deliciouscow/server/internal/store"
	"github.com/johnlrt/deliciouscow/server/internal/types"
)

func buildSessionView(acc *store.Account, ses *store.Session) types.SessionView {
	return types.SessionView{
		AccountID:              acc.ID,
		Username:               acc.Username,
		CSRFToken:              ses.CSRFToken,
		TOTPEnabled:            acc.TOTPEnabled,
		PrivacyNoticeVersion:   acc.PrivacyNoticeVersion,
		SessionExpiresAt:       types.FormatTS(ses.ExpiresAt),
		RecoveryCodesRemaining: len(acc.RecoveryCodeHashes),
	}
}

func (s *Server) createSessionLocked(tx *store.Tx, accountID string, now time.Time) (rawToken string, ses *store.Session, err error) {
	rawToken, err = auth.RandomHex(32)
	if err != nil {
		return "", nil, err
	}
	csrf, err := auth.RandomHex(24)
	if err != nil {
		return "", nil, err
	}
	ses = &store.Session{
		ID:        uuid.NewString(),
		AccountID: accountID,
		TokenHash: auth.SHA256Hex(rawToken),
		CSRFToken: csrf,
		CreatedAt: now,
		LastSeen:  now,
		ExpiresAt: now.Add(12 * time.Hour),
		AbsExpiry: now.Add(7 * 24 * time.Hour),
	}
	tx.Sessions[ses.TokenHash] = ses
	return rawToken, ses, nil
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username             string `json:"username"`
		Password             string `json:"password"`
		PrivacyNoticeVersion string `json:"privacy_notice_version"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, r, "invalid_request", "invalid json", nil)
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if len(req.Username) < 3 || len(req.Username) > 64 {
		writeErr(w, r, "invalid_request", "username must be 3-64 characters", types.ErrPtr("username"))
		return
	}
	if len(req.Password) < 12 || len(req.Password) > 128 {
		writeErr(w, r, "invalid_request", "password must be 12-128 characters", types.ErrPtr("password"))
		return
	}
	if req.PrivacyNoticeVersion == "" {
		writeErr(w, r, "invalid_request", "privacy_notice_version is required", types.ErrPtr("privacy_notice_version"))
		return
	}
	pwHash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeErr(w, r, "server_error", "internal error", nil)
		return
	}
	plainCodes, codeHashes, err := auth.GenerateRecoveryCodes(8)
	if err != nil {
		writeErr(w, r, "server_error", "internal error", nil)
		return
	}
	now := s.Store.Now()
	var rawTok string
	var view types.SessionView
	err = s.Store.WithLock(func(tx *store.Tx) error {
		if _, exists := tx.UsernameIndex[strings.ToLower(req.Username)]; exists {
			return store.ErrAlreadyExists
		}
		accID := uuid.NewString()
		acc := &store.Account{
			ID:                   accID,
			Username:             req.Username,
			PasswordHash:         pwHash,
			PrivacyNoticeVersion: req.PrivacyNoticeVersion,
			RecoveryCodeHashes:   codeHashes,
			BalanceUpdatedAt:     now,
			CreatedAt:            now,
		}
		tx.Accounts[accID] = acc
		tx.UsernameIndex[strings.ToLower(req.Username)] = accID
		var ses *store.Session
		var err error
		rawTok, ses, err = s.createSessionLocked(tx, accID, now)
		if err != nil {
			return err
		}
		view = buildSessionView(acc, ses)
		return nil
	})
	if err == store.ErrAlreadyExists {
		writeErr(w, r, "conflict", "username already exists", types.ErrPtr("username"))
		return
	}
	if err != nil {
		writeErr(w, r, "server_error", "internal error", nil)
		return
	}
	s.setSessionCookie(w, r, rawTok)
	writeJSON(w, http.StatusCreated, types.RegisterResponse{
		SessionView:   view,
		RecoveryCodes: plainCodes,
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		TOTPCode string `json:"totp_code"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, r, "invalid_request", "invalid json", nil)
		return
	}
	now := s.Store.Now()
	var rawTok string
	var view types.SessionView
	err := s.Store.WithLock(func(tx *store.Tx) error {
		accID, ok := tx.UsernameIndex[strings.ToLower(strings.TrimSpace(req.Username))]
		if !ok {
			return store.ErrNotFound
		}
		acc := tx.Accounts[accID]
		if !auth.VerifyPassword(req.Password, acc.PasswordHash) {
			return store.ErrNotFound
		}
		if acc.TOTPEnabled {
			if req.TOTPCode == "" || !auth.VerifyTOTP(acc.TOTPSecret, req.TOTPCode, now) {
				return store.ErrNotFound
			}
		}
		var ses *store.Session
		var err error
		rawTok, ses, err = s.createSessionLocked(tx, accID, now)
		if err != nil {
			return err
		}
		view = buildSessionView(acc, ses)
		return nil
	})
	if err != nil {
		writeErr(w, r, "unauthenticated", "invalid credentials", nil)
		return
	}
	s.setSessionCookie(w, r, rawTok)
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleRecover(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username     string `json:"username"`
		RecoveryCode string `json:"recovery_code"`
		NewPassword  string `json:"new_password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, r, "invalid_request", "invalid json", nil)
		return
	}
	if len(req.NewPassword) < 12 || len(req.NewPassword) > 128 {
		writeErr(w, r, "invalid_request", "new_password must be 12-128 characters", types.ErrPtr("new_password"))
		return
	}
	pwHash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		writeErr(w, r, "server_error", "internal error", nil)
		return
	}
	codeHash := auth.SHA256Hex(strings.TrimSpace(req.RecoveryCode))
	now := s.Store.Now()
	var rawTok string
	var view types.SessionView
	err = s.Store.WithLock(func(tx *store.Tx) error {
		accID, ok := tx.UsernameIndex[strings.ToLower(strings.TrimSpace(req.Username))]
		if !ok {
			return store.ErrNotFound
		}
		acc := tx.Accounts[accID]
		matchedIdx := -1
		for i, h := range acc.RecoveryCodeHashes {
			if subtle.ConstantTimeCompare([]byte(h), []byte(codeHash)) == 1 {
				matchedIdx = i
				break
			}
		}
		if matchedIdx < 0 {
			return store.ErrNotFound
		}
		// Consume recovery code, revoke all existing sessions, reset TOTP, lock payouts 24h
		acc.RecoveryCodeHashes = append(acc.RecoveryCodeHashes[:matchedIdx], acc.RecoveryCodeHashes[matchedIdx+1:]...)
		acc.PasswordHash = pwHash
		acc.TOTPEnabled = false
		acc.TOTPSecret = ""
		elig := now.Add(24 * time.Hour)
		acc.PayoutEligibleAfter = &elig
		for th, ses := range tx.Sessions {
			if ses.AccountID == accID {
				delete(tx.Sessions, th)
			}
		}
		var ses *store.Session
		rawTok, ses, err = s.createSessionLocked(tx, accID, now)
		if err != nil {
			return err
		}
		view = buildSessionView(acc, ses)
		return nil
	})
	if err != nil {
		writeErr(w, r, "unauthenticated", "invalid username or recovery code", nil)
		return
	}
	s.setSessionCookie(w, r, rawTok)
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	ses, err := s.resolveSession(r)
	if err != nil {
		writeErr(w, r, "unauthenticated", "session expired or missing", nil)
		return
	}
	var view types.SessionView
	_ = s.Store.WithReadLock(func(tx *store.Tx) error {
		acc := tx.Accounts[ses.AccountID]
		view = buildSessionView(acc, ses)
		return nil
	})
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	ses, ok := s.requireSessionCSRF(w, r)
	if !ok {
		return
	}
	_ = s.Store.WithLock(func(tx *store.Tx) error {
		delete(tx.Sessions, ses.TokenHash)
		return nil
	})
	s.clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTOTPSetup(w http.ResponseWriter, r *http.Request) {
	ses, ok := s.requireSessionCSRF(w, r)
	if !ok {
		return
	}
	var req struct {
		Password string `json:"password"`
		TOTPCode string `json:"totp_code"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, r, "invalid_request", "invalid json", nil)
		return
	}
	now := s.Store.Now()
	secret, _ := auth.RandomBase32(20)
	setupID := uuid.NewString()
	expiresAt := now.Add(10 * time.Minute)
	var uri string
	err := s.Store.WithLock(func(tx *store.Tx) error {
		acc := tx.Accounts[ses.AccountID]
		if !auth.VerifyPassword(req.Password, acc.PasswordHash) {
			return store.ErrNotFound
		}
		if acc.TOTPEnabled && !auth.VerifyTOTP(acc.TOTPSecret, req.TOTPCode, now) {
			return store.ErrNotFound
		}
		acc.PendingTOTPSetupID = setupID
		acc.PendingTOTPSecret = secret
		acc.PendingTOTPExpiresAt = &expiresAt
		uri = auth.BuildOTPAuthURI(acc.Username, secret)
		return nil
	})
	if err != nil {
		writeErr(w, r, "unauthenticated", "invalid credentials", nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"setup_id":   setupID,
		"otpauth_uri": uri,
		"expires_at": types.FormatTS(expiresAt),
	})
}

func (s *Server) handleTOTPConfirm(w http.ResponseWriter, r *http.Request) {
	ses, ok := s.requireSessionCSRF(w, r)
	if !ok {
		return
	}
	var req struct {
		SetupID string `json:"setup_id"`
		Code    string `json:"code"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, r, "invalid_request", "invalid json", nil)
		return
	}
	now := s.Store.Now()
	err := s.Store.WithLock(func(tx *store.Tx) error {
		acc := tx.Accounts[ses.AccountID]
		if acc.PendingTOTPSetupID == "" || acc.PendingTOTPSetupID != req.SetupID || acc.PendingTOTPExpiresAt == nil || now.After(*acc.PendingTOTPExpiresAt) {
			return store.ErrNotFound
		}
		if !auth.VerifyTOTP(acc.PendingTOTPSecret, req.Code, now) {
			return store.ErrConflict
		}
		acc.TOTPEnabled = true
		acc.TOTPSecret = acc.PendingTOTPSecret
		acc.PendingTOTPSetupID = ""
		acc.PendingTOTPSecret = ""
		acc.PendingTOTPExpiresAt = nil
		return nil
	})
	if err != nil {
		writeErr(w, r, "invalid_request", "invalid or expired totp setup/code", nil)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleStepUp(w http.ResponseWriter, r *http.Request) {
	ses, ok := s.requireSessionCSRF(w, r)
	if !ok {
		return
	}
	var req struct {
		Password string `json:"password"`
		TOTPCode string `json:"totp_code"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, r, "invalid_request", "invalid json", nil)
		return
	}
	now := s.Store.Now()
	raw, _ := auth.RandomHex(24)
	exp := now.Add(5 * time.Minute)
	err := s.Store.WithLock(func(tx *store.Tx) error {
		acc := tx.Accounts[ses.AccountID]
		if !auth.VerifyPassword(req.Password, acc.PasswordHash) {
			return store.ErrNotFound
		}
		if acc.TOTPEnabled && !auth.VerifyTOTP(acc.TOTPSecret, req.TOTPCode, now) {
			return store.ErrNotFound
		}
		th := auth.SHA256Hex(raw)
		tx.StepUpTokens[th] = &store.StepUpToken{
			TokenHash: th,
			SessionID: ses.ID,
			AccountID: ses.AccountID,
			ExpiresAt: exp,
		}
		return nil
	})
	if err != nil {
		writeErr(w, r, "unauthenticated", "step-up verification failed", nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"step_up_token": raw,
		"expires_at":    types.FormatTS(exp),
	})
}

func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	ses, err := s.resolveSession(r)
	if err != nil {
		writeErr(w, r, "unauthenticated", "authentication required", nil)
		return
	}
	var out []types.APIKey
	_ = s.Store.WithReadLock(func(tx *store.Tx) error {
		for _, k := range tx.APIKeys {
			if k.AccountID != ses.AccountID || k.RevokedAt != nil {
				continue
			}
			out = append(out, types.APIKey{
				ID:                    k.ID,
				Name:                  k.Name,
				Prefix:                k.Prefix,
				Permissions:           k.Permissions,
				ModelIDs:              k.ModelIDs,
				DailySpendCapPiconero: k.DailySpendCapPiconero,
				CreatedAt:             types.FormatTS(k.CreatedAt),
				LastUsedAt:            types.FormatTSPtr(k.LastUsedAt),
				RevokedAt:             types.FormatTSPtr(k.RevokedAt),
			})
		}
		return nil
	})
	if out == nil {
		out = []types.APIKey{}
	}
	writeJSON(w, http.StatusOK, types.Page[types.APIKey]{Data: out, NextCursor: nil})
}

func (s *Server) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	ses, ok := s.requireSessionCSRF(w, r)
	if !ok {
		return
	}
	var req struct {
		Name                  string       `json:"name"`
		Permissions           []string     `json:"permissions"`
		ModelIDs              []string     `json:"model_ids"`
		DailySpendCapPiconero *types.Money `json:"daily_spend_cap_piconero"`
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
	if len(req.Permissions) == 0 {
		req.Permissions = []string{"chat:write", "usage:read"}
	}
	randPart, _ := auth.RandomHex(24)
	secret := "cvk_" + randPart
	verifier := s.Auth.HashWithPepper(secret)
	now := s.Store.Now()
	id := uuid.NewString()
	rec := &store.APIKeyRecord{
		ID:                    id,
		AccountID:             ses.AccountID,
		Name:                  req.Name,
		Prefix:                secret[:12],
		VerifierHash:          verifier,
		Permissions:           req.Permissions,
		ModelIDs:              req.ModelIDs,
		DailySpendCapPiconero: req.DailySpendCapPiconero,
		CreatedAt:             now,
	}
	_ = s.Store.WithLock(func(tx *store.Tx) error {
		tx.APIKeys[id] = rec
		tx.KeyHashIndex[verifier] = id
		return nil
	})
	writeJSON(w, http.StatusCreated, map[string]any{
		"key": types.APIKey{
			ID:                    rec.ID,
			Name:                  rec.Name,
			Prefix:                rec.Prefix,
			Permissions:           rec.Permissions,
			ModelIDs:              rec.ModelIDs,
			DailySpendCapPiconero: rec.DailySpendCapPiconero,
			CreatedAt:             types.FormatTS(rec.CreatedAt),
		},
		"secret": secret,
	})
}

func (s *Server) handleDeleteKey(w http.ResponseWriter, r *http.Request) {
	ses, ok := s.requireSessionCSRF(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	now := s.Store.Now()
	_ = s.Store.WithLock(func(tx *store.Tx) error {
		if k, exists := tx.APIKeys[id]; exists && k.AccountID == ses.AccountID && k.RevokedAt == nil {
			k.RevokedAt = &now
		}
		return nil
	})
	w.WriteHeader(http.StatusNoContent)
}
