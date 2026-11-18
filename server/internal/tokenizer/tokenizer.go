package tokenizer

import (
	"strings"
	"unicode/utf8"

	"github.com/johnlrt/deliciouscow/server/internal/types"
)

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Tokenizer defines the trusted server-side tokenization boundary (C9).
// In M1, SimpleEstimator provides deterministic token counts; production swaps in llama.cpp helper.
type Tokenizer interface {
	CountInput(modelID string, messages []ChatMessage) (int, []int, error)
	CountOutput(modelID string, text string) (int, error)
}

type SimpleEstimator struct{}

func (SimpleEstimator) CountInput(_ string, messages []ChatMessage) (int, []int, error) {
	var b strings.Builder
	for _, m := range messages {
		b.WriteString(m.Role)
		b.WriteString(": ")
		b.WriteString(m.Content)
		b.WriteByte('\n')
	}
	runes := utf8.RuneCountInString(b.String())
	n := (runes + 3) / 4
	if n < 1 {
		n = 1
	}
	// Synthetic deterministic token IDs for M1 wire contract
	ids := make([]int, n)
	for i := range ids {
		ids[i] = 100 + (i % 30000)
	}
	return n, ids, nil
}

func (SimpleEstimator) CountOutput(_ string, text string) (int, error) {
	if text == "" {
		return 0, nil
	}
	runes := utf8.RuneCountInString(text)
	n := (runes + 3) / 4
	if n < 1 {
		n = 1
	}
	return n, nil
}

func DefaultCatalog() []types.Model {
	return []types.Model{
		{
			ID:                       "cordova-tiny-chat",
			Name:                     "Cordova Tiny Chat (M1)",
			ContextLength:            8192,
			MaxOutputTokens:          2048,
			InputPricePer1MPiconero:  100_000_000, // 0.0001 XMR per 1M input tokens
			OutputPricePer1MPiconero: 200_000_000, // 0.0002 XMR per 1M output tokens
			OwnerShareBPS:            8000,        // 80%
			Availability:             "unavailable",
		},
		{
			ID:                       "llama-3-8b-instruct",
			Name:                     "Llama 3 8B Instruct",
			ContextLength:            8192,
			MaxOutputTokens:          4096,
			InputPricePer1MPiconero:  500_000_000,
			OutputPricePer1MPiconero: 1_000_000_000,
			OwnerShareBPS:            8000,
			Availability:             "unavailable",
		},
	}
}
