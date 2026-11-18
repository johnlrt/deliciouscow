package main

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/johnlrt/deliciouscow/server/internal/tokenizer"
)

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	addr := getenv("ADDR", ":8090")
	secret := os.Getenv("TOKENIZER_SECRET")
	catalogVer := getenv("CATALOG_VERSION", "v1-m1")
	tok := tokenizer.SimpleEstimator{}

	authOK := func(r *http.Request) bool {
		if secret == "" {
			return true
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		return subtle.ConstantTimeCompare([]byte(got), []byte(secret)) == 1
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	})
	mux.HandleFunc("POST /internal/tokenizer/prepare", func(w http.ResponseWriter, r *http.Request) {
		if !authOK(r) {
			http.Error(w, `{"error":{"code":"unauthenticated"}}`, http.StatusUnauthorized)
			return
		}
		var req struct {
			ModelID        string                  `json:"model_id"`
			CatalogVersion string                  `json:"catalog_version"`
			Messages       []tokenizer.ChatMessage `json:"messages"`
		}
		b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil || json.Unmarshal(b, &req) != nil {
			http.Error(w, `{"error":{"code":"invalid_request"}}`, http.StatusBadRequest)
			return
		}
		if req.CatalogVersion != "" && req.CatalogVersion != catalogVer {
			http.Error(w, `{"error":{"code":"conflict"}}`, http.StatusConflict)
			return
		}
		n, ids, err := tok.CountInput(req.ModelID, req.Messages)
		if err != nil {
			http.Error(w, `{"error":{"code":"tokenizer_unavailable"}}`, http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"input_token_ids":  ids,
			"input_tokens":     n,
			"context_tokens":   n,
			"tokenizer_sha256": "simple-estimator-v1",
			"template_sha256":  "simple-template-v1",
		})
	})
	mux.HandleFunc("POST /internal/tokenizer/count", func(w http.ResponseWriter, r *http.Request) {
		if !authOK(r) {
			http.Error(w, `{"error":{"code":"unauthenticated"}}`, http.StatusUnauthorized)
			return
		}
		var req struct {
			ModelID        string `json:"model_id"`
			CatalogVersion string `json:"catalog_version"`
			Text           string `json:"text"`
		}
		b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil || json.Unmarshal(b, &req) != nil {
			http.Error(w, `{"error":{"code":"invalid_request"}}`, http.StatusBadRequest)
			return
		}
		n, err := tok.CountOutput(req.ModelID, req.Text)
		if err != nil {
			http.Error(w, `{"error":{"code":"tokenizer_unavailable"}}`, http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"tokens": n})
	})

	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("cordova-tokenizer listening on %s", addr)
	log.Fatal(srv.ListenAndServe())
}
