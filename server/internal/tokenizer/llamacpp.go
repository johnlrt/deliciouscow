package tokenizer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// HelperClient implements Tokenizer by calling the private C9 llama.cpp tokenizer helper service.
type HelperClient struct {
	Endpoint       string
	Secret         string
	CatalogVersion string
	Client         *http.Client
}

func NewHelperClient(endpoint, secret, catalogVersion string) *HelperClient {
	return &HelperClient{
		Endpoint:       strings.TrimRight(endpoint, "/"),
		Secret:         secret,
		CatalogVersion: catalogVersion,
		Client:         &http.Client{Timeout: 2 * time.Second},
	}
}

func (c *HelperClient) CountInput(modelID string, messages []ChatMessage) (int, []int, error) {
	body, err := json.Marshal(map[string]any{
		"model_id":        modelID,
		"catalog_version": c.CatalogVersion,
		"messages":        messages,
	})
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, c.Endpoint+"/internal/tokenizer/prepare", bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.Secret)
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, nil, fmt.Errorf("tokenizer prepare returned %d", resp.StatusCode)
	}
	var out struct {
		InputTokenIDs []int `json:"input_token_ids"`
		InputTokens   int   `json:"input_tokens"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, nil, err
	}
	return out.InputTokens, out.InputTokenIDs, nil
}

func (c *HelperClient) CountOutput(modelID string, text string) (int, error) {
	if text == "" {
		return 0, nil
	}
	body, err := json.Marshal(map[string]any{
		"model_id":        modelID,
		"catalog_version": c.CatalogVersion,
		"text":            text,
	})
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, c.Endpoint+"/internal/tokenizer/count", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.Secret)
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("tokenizer count returned %d", resp.StatusCode)
	}
	var out struct {
		Tokens int `json:"tokens"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, err
	}
	return out.Tokens, nil
}
