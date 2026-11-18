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
	n, ids, err := c.CountInput("cordova-tiny-chat", []tokenizer.ChatMessage{{Role: "user", Content: "hi"}})
	if err != nil || n != 3 || len(ids) != 3 {
		t.Fatalf("prepare failed: n=%d ids=%v err=%v", n, ids, err)
	}
	outTok, err := c.CountOutput("cordova-tiny-chat", "hello world!")
	if err != nil || outTok != 5 {
		t.Fatalf("count failed: outTok=%d err=%v", outTok, err)
	}
}
