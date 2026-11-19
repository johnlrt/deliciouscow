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
	Pins           map[string]ModelPin
	Client         *http.Client
}

func NewHelperClient(endpoint, secret, catalogVersion string) *HelperClient {
	return &HelperClient{
		Endpoint:       strings.TrimRight(endpoint, "/"),
		Secret:         secret,
		CatalogVersion: catalogVersion,
		Pins:           make(map[string]ModelPin),
		Client:         &http.Client{Timeout: 2 * time.Second},
	}
}

// WithModelPin registers expected tokenizer and chat-template SHA-256 digests for modelID.
// Tokenization is marked Verified only when the helper returns digests matching this pin.
func (c *HelperClient) WithModelPin(modelID, tokenizerSHA256, templateSHA256 string) *HelperClient {
	if c.Pins == nil {
		c.Pins = make(map[string]ModelPin)
	}
	c.Pins[modelID] = ModelPin{
		TokenizerSHA256: strings.ToLower(strings.TrimSpace(tokenizerSHA256)),
		TemplateSHA256:  strings.ToLower(strings.TrimSpace(templateSHA256)),
	}
	return c
}

// WithModelPins validates and registers a map of operator-configured model pins.
func (c *HelperClient) WithModelPins(pins map[string]ModelPin) (*HelperClient, error) {
	for modelID, pin := range pins {
		if modelID == "" || strings.TrimSpace(modelID) != modelID {
			return nil, fmt.Errorf("invalid model_id %q", modelID)
		}
		if !IsSHA256Hex(pin.TokenizerSHA256) {
			return nil, fmt.Errorf("model %q: invalid tokenizer_sha256", modelID)
		}
		if !IsSHA256Hex(pin.TemplateSHA256) {
			return nil, fmt.Errorf("model %q: invalid template_sha256", modelID)
		}
	}
	if c.Pins == nil {
		c.Pins = make(map[string]ModelPin, len(pins))
	}
	for modelID, pin := range pins {
		c.Pins[modelID] = pin
	}
	return c, nil
}

func (c *HelperClient) PrepareInput(modelID string, messages []ChatMessage) (PrepareResult, error) {
	body, err := json.Marshal(map[string]any{
		"model_id":        modelID,
		"catalog_version": c.CatalogVersion,
		"messages":        messages,
	})
	if err != nil {
		return PrepareResult{}, err
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, c.Endpoint+"/internal/tokenizer/prepare", bytes.NewReader(body))
	if err != nil {
		return PrepareResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.Secret)
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return PrepareResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return PrepareResult{}, fmt.Errorf("tokenizer prepare returned %d", resp.StatusCode)
	}
	var out struct {
		InputTokenIDs   []int  `json:"input_token_ids"`
		InputTokens     int    `json:"input_tokens"`
		TokenizerSHA256 string `json:"tokenizer_sha256"`
		TemplateSHA256  string `json:"template_sha256"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return PrepareResult{}, err
	}
	pin, hasPin := c.Pins[modelID]
	verified := hasPin &&
		IsSHA256Hex(pin.TokenizerSHA256) &&
		IsSHA256Hex(pin.TemplateSHA256) &&
		out.TokenizerSHA256 == pin.TokenizerSHA256 &&
		out.TemplateSHA256 == pin.TemplateSHA256
	return PrepareResult{
		InputTokens:     out.InputTokens,
		InputTokenIDs:   out.InputTokenIDs,
		Verified:        verified,
		TokenizerSHA256: out.TokenizerSHA256,
		TemplateSHA256:  out.TemplateSHA256,
	}, nil
}

func (c *HelperClient) CountInput(modelID string, messages []ChatMessage) (int, []int, error) {
	res, err := c.PrepareInput(modelID, messages)
	if err != nil {
		return 0, nil, err
	}
	return res.InputTokens, res.InputTokenIDs, nil
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
