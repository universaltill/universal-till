// Package ai gives the till assistive AI features (docs repo:
// architecture/ai-integration.md) against a pluggable model backend.
//
// The primary backend is SELF-HOSTED (an Ollama server running an open
// vision model on the shop's own hardware/homelab) — Farshid's direction is
// no paid AI: local models now, a custom model trained on the shop's own
// data later. The Claude API exists only as an optional provider for shops
// that choose it. Offline-first is binding (ADR-0003): nothing here sits on
// the checkout path, every feature degrades to the non-AI experience, and
// callers treat errors as "feature unavailable", never as a sale blocker.
package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// Config comes from the environment. With neither an endpoint nor a key the
// whole feature set is invisible; nothing else in the till changes.
type Config struct {
	Provider string // "ollama" (self-hosted, default) or "claude" (optional)
	Endpoint string // Ollama base URL, e.g. http://localhost:11434
	Model    string
	APIKey   string // claude provider only
}

// FromEnv reads UT_AI_PROVIDER / UT_AI_ENDPOINT / UT_AI_MODEL / UT_AI_API_KEY.
// Provider is inferred when unset: an endpoint means ollama, a key means
// claude, neither means disabled.
func FromEnv() Config {
	cfg := Config{
		Provider: strings.ToLower(strings.TrimSpace(os.Getenv("UT_AI_PROVIDER"))),
		Endpoint: strings.TrimSpace(os.Getenv("UT_AI_ENDPOINT")),
		Model:    strings.TrimSpace(os.Getenv("UT_AI_MODEL")),
		APIKey:   strings.TrimSpace(os.Getenv("UT_AI_API_KEY")),
	}
	if cfg.Provider == "" {
		switch {
		case cfg.Endpoint != "":
			cfg.Provider = "ollama"
		case cfg.APIKey != "":
			cfg.Provider = "claude"
		}
	}
	if cfg.Model == "" {
		switch cfg.Provider {
		case "ollama":
			cfg.Model = "llama3.2-vision"
		case "claude":
			cfg.Model = "claude-haiku-4-5"
		}
	}
	return cfg
}

// CatalogItem is the context the model sees for each active product. Only
// item identity leaves the till process — no sales figures, no customer data
// (and with the self-hosted provider nothing leaves the shop at all).
type CatalogItem struct {
	ID   string `json:"id"`
	SKU  string `json:"sku"`
	Name string `json:"name"`
}

// RefImage is a reference photo for one item: the catalog thumbnail, plus
// any cashier-confirmed identification photos (role "ai_ref"). Confirmed
// photos are also the training corpus for a future custom per-shop model.
type RefImage struct {
	ItemID    string
	MediaType string // image/png or image/jpeg
	Data      []byte
}

// Candidate is one proposed match, best first.
type Candidate struct {
	ItemID     string `json:"item_id"`
	Confidence string `json:"confidence"` // high | medium | low
}

// IdentifyResult is the model's structured answer. SuggestedName is filled
// when the product looks like it isn't in the catalog at all ("ask and add").
type IdentifyResult struct {
	Matches       []Candidate `json:"matches"`
	SuggestedName string      `json:"suggested_name"`
}

// provider is one model backend. Implementations must respect ctx deadlines
// and return an error rather than block the caller.
type provider interface {
	identify(ctx context.Context, photo []byte, photoMediaType string, items []CatalogItem, refs []RefImage) (*IdentifyResult, error)
}

// Service is the till-side AI facade. A nil or disabled Service is safe to
// call Enabled() on, so pages can decide whether to render AI affordances.
type Service struct {
	p       provider
	enabled bool
}

func New(cfg Config) *Service {
	switch cfg.Provider {
	case "ollama":
		if cfg.Endpoint == "" {
			return &Service{}
		}
		return &Service{p: newOllamaProvider(cfg.Endpoint, cfg.Model), enabled: true}
	case "claude":
		if cfg.APIKey == "" {
			return &Service{}
		}
		return &Service{p: newClaudeProvider(cfg.APIKey, cfg.Model), enabled: true}
	default:
		return &Service{}
	}
}

func (s *Service) Enabled() bool { return s != nil && s.enabled }

const identifyTimeout = 90 * time.Second // local models on modest hardware are slow

// identifySchema constrains the response so parsing can't fail on prose.
// Both Ollama ("format") and the Claude API (output_config.format) accept it.
var identifySchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"matches": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"item_id":    map[string]any{"type": "string"},
					"confidence": map[string]any{"type": "string", "enum": []string{"high", "medium", "low"}},
				},
				"required":             []string{"item_id", "confidence"},
				"additionalProperties": false,
			},
		},
		"suggested_name": map[string]any{"type": "string"},
	},
	"required":             []string{"matches", "suggested_name"},
	"additionalProperties": false,
}

// identifyPrompt is the shared task description; the catalog rides along as
// JSON so the model can only pick real items.
func identifyPrompt(items []CatalogItem) (string, error) {
	catalogJSON, err := json.Marshal(items)
	if err != nil {
		return "", err
	}
	return "You identify retail products for a point-of-sale till. " +
		"The shop's catalog of active items follows as JSON (id, sku, name). " +
		"Given the photo taken at the till, return the catalog items most likely " +
		"to be the product in the photo, best match first, at most 3 matches. " +
		"Only return item_id values that exist in the catalog. If the product is clearly " +
		"not in the catalog, return no matches and suggest a short product name in suggested_name; " +
		"otherwise leave suggested_name empty.\n\nCatalog:\n" + string(catalogJSON), nil
}

// Identify asks the configured backend for the top matches for a till photo.
func (s *Service) Identify(ctx context.Context, photo []byte, photoMediaType string, items []CatalogItem, refs []RefImage) (*IdentifyResult, error) {
	if !s.Enabled() {
		return nil, fmt.Errorf("ai disabled")
	}
	if len(photo) == 0 {
		return nil, fmt.Errorf("photo required")
	}
	ctx, cancel := context.WithTimeout(ctx, identifyTimeout)
	defer cancel()
	out, err := s.p.identify(ctx, photo, photoMediaType, items, refs)
	if err != nil {
		return nil, err
	}
	// The schema guarantees shape, not referential integrity — drop ids the
	// model hallucinated outside the catalog.
	valid := make(map[string]bool, len(items))
	for _, it := range items {
		valid[it.ID] = true
	}
	kept := out.Matches[:0]
	for _, m := range out.Matches {
		if valid[m.ItemID] {
			kept = append(kept, m)
		}
	}
	out.Matches = kept
	return out, nil
}
