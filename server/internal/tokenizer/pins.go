package tokenizer

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

// ModelPinEntry is a single operator-configured model pin in the JSON pins file.
type ModelPinEntry struct {
	ModelID         string `json:"model_id"`
	TokenizerSHA256 string `json:"tokenizer_sha256"`
	TemplateSHA256  string `json:"template_sha256"`
}

// ModelPinsConfig is the top-level JSON document for operator-configured model pins.
type ModelPinsConfig struct {
	Models []ModelPinEntry `json:"models"`
}

// ParseModelPins parses and validates an operator model-pins JSON document.
// All digests must be canonical 64-char lowercase hex SHA-256 values and model_id entries must be unique.
func ParseModelPins(r io.Reader) (map[string]ModelPin, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()

	var cfg ModelPinsConfig
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode model pins JSON: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("unexpected trailing data in model pins JSON")
	}
	if cfg.Models == nil {
		return nil, fmt.Errorf("model pins JSON must contain a \"models\" array")
	}

	pins := make(map[string]ModelPin, len(cfg.Models))
	for i, entry := range cfg.Models {
		if entry.ModelID == "" || strings.TrimSpace(entry.ModelID) != entry.ModelID {
			return nil, fmt.Errorf("model pin[%d]: invalid or empty model_id %q", i, entry.ModelID)
		}
		if _, dup := pins[entry.ModelID]; dup {
			return nil, fmt.Errorf("model pin[%d]: duplicate model_id %q", i, entry.ModelID)
		}
		if !IsSHA256Hex(entry.TokenizerSHA256) {
			return nil, fmt.Errorf("model pin %q: tokenizer_sha256 must be 64-char lowercase hex SHA-256", entry.ModelID)
		}
		if !IsSHA256Hex(entry.TemplateSHA256) {
			return nil, fmt.Errorf("model pin %q: template_sha256 must be 64-char lowercase hex SHA-256", entry.ModelID)
		}
		pins[entry.ModelID] = ModelPin{
			TokenizerSHA256: entry.TokenizerSHA256,
			TemplateSHA256:  entry.TemplateSHA256,
		}
	}
	return pins, nil
}

// LoadModelPinsFile reads and validates model pins from an operator-managed JSON file.
func LoadModelPinsFile(path string) (map[string]ModelPin, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat model pins file %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("model pins path %q must be a regular file", path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o002 != 0 {
		return nil, fmt.Errorf("model pins file %q must not be world-writable (mode %04o)", path, info.Mode().Perm())
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open model pins file %q: %w", path, err)
	}
	defer f.Close()
	return ParseModelPins(f)
}
