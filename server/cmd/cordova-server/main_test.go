package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/johnlrt/deliciouscow/server/internal/tokenizer"
)

func TestConfigureTokenizerModes(t *testing.T) {
	// 1. Default dev/mock mode (no TOKENIZER_URL, no pins): SimpleEstimator, non-billable
	tok, count, err := configureTokenizer("", "", "v1-m1", "")
	if err != nil {
		t.Fatalf("default mode failed: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 pins, got %d", count)
	}
	prep, err := tokenizer.PrepareInput(tok, "cordova-tiny-chat", []tokenizer.ChatMessage{{Role: "user", Content: "hi"}})
	if err != nil || prep.Verified {
		t.Fatalf("expected SimpleEstimator Verified=false, got %+v err=%v", prep, err)
	}

	// 2. Pins file without TOKENIZER_URL must fail fast
	dir := t.TempDir()
	tokHash := strings.Repeat("c", 64)
	tplHash := strings.Repeat("d", 64)
	pinsPath := filepath.Join(dir, "pins.json")
	if err := os.WriteFile(pinsPath, []byte(`{"models":[{"model_id":"llama-3-8b-instruct","tokenizer_sha256":"`+tokHash+`","template_sha256":"`+tplHash+`"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := configureTokenizer("", "", "v1-m1", pinsPath); err == nil {
		t.Fatal("expected error when TOKENIZER_MODEL_PINS_FILE is set without TOKENIZER_URL")
	}

	// 3. Malformed pins file must fail fast
	badPath := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(badPath, []byte(`{"models":[{"model_id":"m","tokenizer_sha256":"bad","template_sha256":"bad"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := configureTokenizer("http://127.0.0.1:8090", "sec", "v1-m1", badPath); err == nil {
		t.Fatal("expected error for malformed pins file")
	}

	// 4. HelperClient with valid pins file verifies matching model and rejects unpinned/mismatched
	var respTok, respTpl string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"input_token_ids":  []int{1, 2, 3},
			"input_tokens":     3,
			"tokenizer_sha256": respTok,
			"template_sha256":  respTpl,
		})
	}))
	defer ts.Close()

	tok, count, err = configureTokenizer(ts.URL, "sec", "v1-m1", pinsPath)
	if err != nil {
		t.Fatalf("valid pins config failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 pin, got %d", count)
	}

	respTok, respTpl = tokHash, tplHash
	prep, err = tokenizer.PrepareInput(tok, "llama-3-8b-instruct", []tokenizer.ChatMessage{{Role: "user", Content: "hi"}})
	if err != nil || !prep.Verified {
		t.Fatalf("expected Verified=true for matching pin, got %+v err=%v", prep, err)
	}

	respTok, respTpl = strings.Repeat("e", 64), tplHash
	prep, err = tokenizer.PrepareInput(tok, "llama-3-8b-instruct", []tokenizer.ChatMessage{{Role: "user", Content: "hi"}})
	if err != nil || prep.Verified {
		t.Fatalf("expected Verified=false for tokenizer mismatch, got %+v err=%v", prep, err)
	}
}
