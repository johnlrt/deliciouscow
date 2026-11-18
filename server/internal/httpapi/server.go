package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/johnlrt/deliciouscow/server/internal/auth"
	"github.com/johnlrt/deliciouscow/server/internal/store"
	"github.com/johnlrt/deliciouscow/server/internal/tokenizer"
	"github.com/johnlrt/deliciouscow/server/internal/types"
	"github.com/johnlrt/deliciouscow/server/internal/wallet"
	"github.com/johnlrt/deliciouscow/server/internal/ws"
)

type Server struct {
	Store       store.Store
	Auth        *auth.Config
	Tok         tokenizer.Tokenizer
	Wallet      wallet.Wallet
	Hub         *ws.Hub
	Catalog     []types.Model
	DevMode     bool
	StaticDir   string
	mux         *http.ServeMux
}

func NewServer(st store.Store, authCfg *auth.Config, tok tokenizer.Tokenizer, wal wallet.Wallet, hub *ws.Hub, catalog []types.Model, devMode bool, staticDir string) *Server {
	s := &Server{
		Store:     st,
		Auth:      authCfg,
		Tok:       tok,
		Wallet:    wal,
		Hub:       hub,
		Catalog:   catalog,
		DevMode:   devMode,
		StaticDir: staticDir,
		mux:       http.NewServeMux(),
	}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	reqID := r.Header.Get("X-Request-ID")
	if reqID == "" {
		reqID = uuid.NewString()
	}
	w.Header().Set("X-Request-ID", reqID)
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("GET /ready", s.handleHealth)

	// Auth
	s.mux.HandleFunc("POST /api/auth/register", s.handleRegister)
	s.mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/auth/recover", s.handleRecover)
	s.mux.HandleFunc("GET /api/session", s.handleGetSession)
	s.mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	s.mux.HandleFunc("POST /api/auth/totp/setup", s.handleTOTPSetup)
	s.mux.HandleFunc("POST /api/auth/totp/confirm", s.handleTOTPConfirm)
	s.mux.HandleFunc("POST /api/auth/step-up", s.handleStepUp)

	// API Keys
	s.mux.HandleFunc("GET /api/keys", s.handleListKeys)
	s.mux.HandleFunc("POST /api/keys", s.handleCreateKey)
	s.mux.HandleFunc("DELETE /api/keys/{id}", s.handleDeleteKey)

	// Nodes & Enrollment
	s.mux.HandleFunc("GET /api/nodes", s.handleListNodes)
	s.mux.HandleFunc("GET /api/nodes/{id}", s.handleGetNode)
	s.mux.HandleFunc("POST /api/nodes/link", s.handleLinkNode)
	s.mux.HandleFunc("PATCH /api/nodes/{id}", s.handlePatchNode)
	s.mux.HandleFunc("POST /api/nodes/{id}/revoke", s.handleRevokeNode)
	s.mux.HandleFunc("POST /api/node/enrollments", s.handleCreateEnrollment)
	s.mux.HandleFunc("POST /api/node/enrollments/token", s.handlePollEnrollmentToken)
	s.mux.HandleFunc("POST /api/node/auth/challenge", s.handleNodeChallenge)
	s.mux.HandleFunc("POST /api/node/auth/token", s.handleNodeAuthToken)
	s.mux.HandleFunc("GET /api/node/ws", s.Hub.ServeNodeWS)

	// Live Dashboard WS
	s.mux.HandleFunc("GET /api/live", func(w http.ResponseWriter, r *http.Request) {
		s.Hub.ServeLiveWS(w, r, s.resolveSession)
	})

	// Billing
	s.mux.HandleFunc("GET /api/billing/address", s.handleBillingAddress)
	s.mux.HandleFunc("GET /api/billing/balance", s.handleBillingBalance)
	s.mux.HandleFunc("GET /api/billing/deposits", s.handleBillingDeposits)
	s.mux.HandleFunc("GET /api/billing/history", s.handleBillingHistory)
	s.mux.HandleFunc("GET /api/billing/payout-settings", s.handleGetPayoutSettings)
	s.mux.HandleFunc("PUT /api/billing/payout-settings", s.handlePutPayoutSettings)
	s.mux.HandleFunc("GET /api/billing/payouts", s.handleBillingPayouts)
	if s.DevMode {
		s.mux.HandleFunc("POST /api/dev/deposit", s.handleDevDeposit)
	}

	// Usage & Analytics
	s.mux.HandleFunc("GET /api/usage", s.handleUsage)
	s.mux.HandleFunc("GET /api/analytics", s.handleAnalytics)

	// OpenAI API
	s.mux.HandleFunc("GET /v1/models", s.handleModels)
	s.mux.HandleFunc("POST /v1/chat/completions", s.handleChatCompletions)

	// Static frontend (if built)
	if s.StaticDir != "" {
		s.mux.HandleFunc("/", s.handleStatic)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/v1/") {
		writeErr(w, r, "not_found", "unknown endpoint", nil)
		return
	}
	clean := filepath.Clean(r.URL.Path)
	if clean == "/" {
		clean = "/index.html"
	}
	full := filepath.Join(s.StaticDir, clean)
	if st, err := os.Stat(full); err == nil && !st.IsDir() {
		http.ServeFile(w, r, full)
		return
	}
	// Try .html for SvelteKit static prerendered routes
	if st, err := os.Stat(full + ".html"); err == nil && !st.IsDir() {
		http.ServeFile(w, r, full+".html")
		return
	}
	// Fallback to index.html for SPA routes
	idx := filepath.Join(s.StaticDir, "index.html")
	if _, err := os.Stat(idx); err == nil {
		http.ServeFile(w, r, idx)
		return
	}
	http.NotFound(w, r)
}

func (s *Server) cookieName(r *http.Request) string {
	if s.Auth.InsecureCookies && r.TLS == nil && r.Header.Get("X-Forwarded-Proto") != "https" {
		return "cordova_session"
	}
	return "__Host-cordova_session"
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	secure := !(s.Auth.InsecureCookies && r.TLS == nil && r.Header.Get("X-Forwarded-Proto") != "https")
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName(r),
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   7 * 24 * 3600,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	secure := !(s.Auth.InsecureCookies && r.TLS == nil && r.Header.Get("X-Forwarded-Proto") != "https")
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName(r),
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func (s *Server) readSessionCookie(r *http.Request) string {
	for _, name := range []string{"__Host-cordova_session", "cordova_session"} {
		if c, err := r.Cookie(name); err == nil && c.Value != "" {
			return c.Value
		}
	}
	return ""
}

func (s *Server) resolveSession(r *http.Request) (*store.Session, error) {
	raw := s.readSessionCookie(r)
	if raw == "" {
		return nil, store.ErrNotFound
	}
	th := auth.SHA256Hex(raw)
	now := s.Store.Now()
	var out *store.Session
	err := s.Store.WithLock(func(tx *store.Tx) error {
		ses, ok := tx.Sessions[th]
		if !ok {
			return store.ErrNotFound
		}
		if now.After(ses.ExpiresAt) || now.After(ses.AbsExpiry) {
			delete(tx.Sessions, th)
			return store.ErrNotFound
		}
		// Sliding 12h idle expiry, bounded by 7d absolute expiry
		nextIdle := now.Add(12 * 3600 * 1e9)
		if nextIdle.After(ses.AbsExpiry) {
			nextIdle = ses.AbsExpiry
		}
		ses.LastSeen = now
		ses.ExpiresAt = nextIdle
		cp := *ses
		out = &cp
		return nil
	})
	return out, err
}

func (s *Server) requireSessionCSRF(w http.ResponseWriter, r *http.Request) (*store.Session, bool) {
	ses, err := s.resolveSession(r)
	if err != nil {
		writeErr(w, r, "unauthenticated", "authentication required", nil)
		return nil, false
	}
	csrf := r.Header.Get("X-CSRF-Token")
	if csrf == "" || subtle.ConstantTimeCompare([]byte(csrf), []byte(ses.CSRFToken)) != 1 {
		writeErr(w, r, "csrf_failed", "invalid csrf token", nil)
		return nil, false
	}
	return ses, true
}

func decodeJSON(r *http.Request, dst any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, dst)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, r *http.Request, code, msg string, param *string) {
	status, errType := types.SafeErrorCodeStatus(code)
	reqID := w.Header().Get("X-Request-ID")
	if reqID == "" {
		reqID = uuid.NewString()
	}
	writeJSON(w, status, types.APIError{
		Error: types.APIErrorBody{
			Message: msg,
			Type:    errType,
			Param:   param,
			Code:    code,
		},
		RequestID: reqID,
	})
}
