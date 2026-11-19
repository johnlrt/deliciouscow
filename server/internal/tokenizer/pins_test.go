package tokenizer_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/johnlrt/deliciouscow/server/internal/tokenizer"
)

func TestParseModelPinsValidation(t *testing.T) {
	validTok := strings.Repeat("a", 64)
	validTpl := strings.Repeat("b", 64)

	cases := []struct {
		name    string
		input   string
		wantErr bool
		wantLen int
	}{
		{
			name:    "valid single pin",
			input:   `{"models":[{"model_id":"llama-3-8b-instruct","tokenizer_sha256":"` + validTok + `","template_sha256":"` + validTpl + `"}]}`,
			wantErr: false,
			wantLen: 1,
		},
		{
			name:    "valid empty models list",
			input:   `{"models":[]}`,
			wantErr: false,
			wantLen: 0,
		},
		{
			name:    "malformed json",
			input:   `{"models":[`,
			wantErr: true,
		},
		{
			name:    "missing models key",
			input:   `{}`,
			wantErr: true,
		},
		{
			name:    "unknown field rejected",
			input:   `{"models":[],"extra":1}`,
			wantErr: true,
		},
		{
			name:    "empty model_id",
			input:   `{"models":[{"model_id":"","tokenizer_sha256":"` + validTok + `","template_sha256":"` + validTpl + `"}]}`,
			wantErr: true,
		},
		{
			name:    "duplicate model_id",
			input:   `{"models":[{"model_id":"m1","tokenizer_sha256":"` + validTok + `","template_sha256":"` + validTpl + `"},{"model_id":"m1","tokenizer_sha256":"` + validTok + `","template_sha256":"` + validTpl + `"}]}`,
			wantErr: true,
		},
		{
			name:    "short tokenizer_sha256",
			input:   `{"models":[{"model_id":"m1","tokenizer_sha256":"abcd","template_sha256":"` + validTpl + `"}]}`,
			wantErr: true,
		},
		{
			name:    "uppercase tokenizer_sha256 rejected",
			input:   `{"models":[{"model_id":"m1","tokenizer_sha256":"` + strings.Repeat("A", 64) + `","template_sha256":"` + validTpl + `"}]}`,
			wantErr: true,
		},
		{
			name:    "invalid template_sha256",
			input:   `{"models":[{"model_id":"m1","tokenizer_sha256":"` + validTok + `","template_sha256":"` + strings.Repeat("z", 64) + `"}]}`,
			wantErr: true,
		},
		{
			name:    "trailing json data rejected",
			input:   `{"models":[]} {"extra":true}`,
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pins, err := tokenizer.ParseModelPins(strings.NewReader(tc.input))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (pins=%+v)", pins)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(pins) != tc.wantLen {
				t.Fatalf("expected %d pins, got %d", tc.wantLen, len(pins))
			}
		})
	}
}

func TestLoadModelPinsFileAndHelperVerification(t *testing.T) {
	dir := t.TempDir()
	tokHash := strings.Repeat("1", 64)
	tplHash := strings.Repeat("2", 64)

	// Missing file fails
	if _, err := tokenizer.LoadModelPinsFile(filepath.Join(dir, "does-not-exist.json")); err == nil {
		t.Fatal("expected error for missing pins file")
	}

	// Write valid pins file with restricted permissions
	pinsPath := filepath.Join(dir, "model-pins.json")
	content := `{"models":[{"model_id":"llama-3-8b-instruct","tokenizer_sha256":"` + tokHash + `","template_sha256":"` + tplHash + `"}]}`
	if err := os.WriteFile(pinsPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	// On Unix, verify world-writable file is rejected
	if runtime.GOOS != "windows" {
		badPerms := filepath.Join(dir, "world-writable.json")
		if err := os.WriteFile(badPerms, []byte(content), 0o666); err != nil {
			t.Fatal(err)
		}
		_ = os.Chmod(badPerms, 0o666)
		if _, err := tokenizer.LoadModelPinsFile(badPerms); err == nil {
			t.Fatal("expected error for world-writable pins file")
		}
	}

	pins, err := tokenizer.LoadModelPinsFile(pinsPath)
	if err != nil {
		t.Fatalf("LoadModelPinsFile: %v", err)
	}

	var respTok, respTpl string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"input_token_ids":  []int{101, 102},
			"input_tokens":     2,
			"tokenizer_sha256": respTok,
			"template_sha256":  respTpl,
		})
	}))
	defer ts.Close()

	hc, err := tokenizer.NewHelperClient(ts.URL, "sec", "v1").WithModelPins(pins)
	if err != nil {
		t.Fatalf("WithModelPins: %v", err)
	}
	msgs := []tokenizer.ChatMessage{{Role: "user", Content: "hi"}}

	// 1. Exact match -> Verified=true
	respTok, respTpl = tokHash, tplHash
	res, err := hc.PrepareInput("llama-3-8b-instruct", msgs)
	if err != nil || !res.Verified {
		t.Fatalf("expected Verified=true for exact pin match, got %+v err=%v", res, err)
	}

	// 2. Missing pin for another model -> Verified=false
	res, err = hc.PrepareInput("unpinned-model", msgs)
	if err != nil || res.Verified {
		t.Fatalf("expected Verified=false for unpinned model, got %+v err=%v", res, err)
	}

	// 3. Tokenizer mismatch -> Verified=false
	respTok, respTpl = strings.Repeat("9", 64), tplHash
	res, err = hc.PrepareInput("llama-3-8b-instruct", msgs)
	if err != nil || res.Verified {
		t.Fatalf("expected Verified=false for tokenizer_sha256 mismatch, got %+v err=%v", res, err)
	}

	// 4. Template mismatch -> Verified=false
	respTok, respTpl = tokHash, strings.Repeat("8", 64)
	res, err = hc.PrepareInput("llama-3-8b-instruct", msgs)
	if err != nil || res.Verified {
		t.Fatalf("expected Verified=false for template_sha256 mismatch, got %+v err=%v", res, err)
	}
}
