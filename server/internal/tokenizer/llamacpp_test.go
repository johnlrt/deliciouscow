package tokenizer_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/johnlrt/deliciouscow/server/internal/tokenizer"
)

func TestHelperClientC9(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer helper-secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/internal/tokenizer/prepare":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"input_token_ids": []int{1, 42, 99},
				"input_tokens":    3,
			})
		case "/internal/tokenizer/count":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tokens": 5,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := tokenizer.NewHelperClient(srv.URL, "helper-secret", "v1-m1")
	prep, err := c.PrepareInput("cordova-tiny-chat", []tokenizer.ChatMessage{{Role: "user", Content: "hi"}})
	if err != nil || prep.InputTokens != 3 || len(prep.InputTokenIDs) != 3 {
		t.Fatalf("prepare failed: prep=%+v err=%v", prep, err)
	}
	if prep.Verified {
		t.Fatalf("expected Verified=false when sha256 hashes are absent")
	}
	outTok, err := c.CountOutput("cordova-tiny-chat", "hello world!")
	if err != nil || outTok != 5 {
		t.Fatalf("count failed: outTok=%d err=%v", outTok, err)
	}
}

func TestHelperClientVerifiedTokenization(t *testing.T) {
	tokHash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	tplHash := "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	otherHash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"input_token_ids":  []int{1, 2},
			"input_tokens":     2,
			"tokenizer_sha256": tokHash,
			"template_sha256":  tplHash,
		})
	}))
	defer srv.Close()

	// 1. Valid SHA-256 format WITHOUT a model pin MUST NOT be considered verified.
	unpinned := tokenizer.NewHelperClient(srv.URL, "", "v1-m1")
	prep, err := tokenizer.PrepareInput(unpinned, "llama-3-8b-instruct", []tokenizer.ChatMessage{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("prepare failed: %v", err)
	}
	if prep.Verified {
		t.Fatalf("expected Verified=false when no model pin is configured")
	}

	// 2. Mismatched template pin MUST NOT be considered verified.
	mismatched := tokenizer.NewHelperClient(srv.URL, "", "v1-m1").WithModelPin("llama-3-8b-instruct", tokHash, otherHash)
	prep, err = tokenizer.PrepareInput(mismatched, "llama-3-8b-instruct", []tokenizer.ChatMessage{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("prepare failed: %v", err)
	}
	if prep.Verified {
		t.Fatalf("expected Verified=false when template pin does not match")
	}

	// 3. Matching model + tokenizer + template pin IS verified.
	pinned := tokenizer.NewHelperClient(srv.URL, "", "v1-m1").WithModelPin("llama-3-8b-instruct", tokHash, tplHash)
	prep, err = tokenizer.PrepareInput(pinned, "llama-3-8b-instruct", []tokenizer.ChatMessage{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("prepare failed: %v", err)
	}
	if !prep.Verified {
		t.Fatalf("expected Verified=true when model tokenizer and template pins match")
	}

	// 4. SimpleEstimator is always unverified.
	estPrep, err := tokenizer.PrepareInput(tokenizer.SimpleEstimator{}, "llama-3-8b-instruct", []tokenizer.ChatMessage{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("estimator prepare failed: %v", err)
	}
	if estPrep.Verified {
		t.Fatalf("expected SimpleEstimator to report Verified=false")
	}
}
