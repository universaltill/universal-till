// Package ai wraps the Anthropic API for the till's assistive AI features
// (docs repo: architecture/ai-integration.md). Offline-first is binding
// (ADR-0003): nothing in this package sits on the checkout path — every
// feature degrades to the non-AI experience when there is no key or no
// network, and callers must treat errors as "feature unavailable", never as
// a sale blocker.
package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Config comes from the environment. No key → the whole feature set is
// invisible; nothing else in the till changes.
type Config struct {
	APIKey string
	Model  string
}

// FromEnv reads UT_AI_API_KEY / UT_AI_MODEL. The default model is Haiku:
// per-shop cost posture is "cheap or free" (a camera identification is
// roughly half a cent); shops that want maximum quality can override.
func FromEnv() Config {
	model := strings.TrimSpace(os.Getenv("UT_AI_MODEL"))
	if model == "" {
		model = string(anthropic.ModelClaudeHaiku4_5)
	}
	return Config{
		APIKey: strings.TrimSpace(os.Getenv("UT_AI_API_KEY")),
		Model:  model,
	}
}

// Service is the till-side AI client. A nil or disabled Service is safe to
// call Enabled() on, so pages can decide whether to render AI affordances.
type Service struct {
	client  anthropic.Client
	model   anthropic.Model
	enabled bool
}

func New(cfg Config) *Service {
	if cfg.APIKey == "" {
		return &Service{}
	}
	return &Service{
		client:  anthropic.NewClient(option.WithAPIKey(cfg.APIKey)),
		model:   anthropic.Model(cfg.Model),
		enabled: true,
	}
}

func (s *Service) Enabled() bool { return s != nil && s.enabled }

// CatalogItem is the context the model sees for each active product. Only
// item identity leaves the till — no sales figures, no customer data.
type CatalogItem struct {
	ID   string `json:"id"`
	SKU  string `json:"sku"`
	Name string `json:"name"`
}

// RefImage is a reference photo for one item: the catalog thumbnail, plus
// any cashier-confirmed identification photos (role "ai_ref") — the
// per-shop "training" loop with no fine-tuning.
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

const identifyTimeout = 45 * time.Second

// identifySchema constrains the response so parsing can't fail on prose.
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

// Identify sends the captured photo plus the shop's own catalog context and
// asks for the top matches. The catalog text and reference images form a
// stable prefix with a cache breakpoint on its last block, so repeated
// identifications within the cache TTL re-read it at ~10% of the cost.
func (s *Service) Identify(ctx context.Context, photo []byte, photoMediaType string, items []CatalogItem, refs []RefImage) (*IdentifyResult, error) {
	if !s.Enabled() {
		return nil, fmt.Errorf("ai disabled")
	}
	if len(photo) == 0 {
		return nil, fmt.Errorf("photo required")
	}
	catalogJSON, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}

	system := []anthropic.TextBlockParam{{
		Text: "You identify retail products for a point-of-sale till. " +
			"The shop's catalog of active items follows as JSON (id, sku, name). " +
			"Given reference images and a photo taken at the till, return the catalog items " +
			"most likely to be the product in the photo, best match first, at most 3 matches. " +
			"Only return item_id values that exist in the catalog. If the product is clearly " +
			"not in the catalog, return no matches and suggest a short product name in suggested_name; " +
			"otherwise leave suggested_name empty.\n\nCatalog:\n" + string(catalogJSON),
	}}

	// Stable prefix: labelled reference images, breakpoint on the last one
	// (or on the system block when there are no references).
	var prefix []anthropic.ContentBlockParamUnion
	for _, ref := range refs {
		prefix = append(prefix,
			anthropic.NewTextBlock("Reference image for item "+ref.ItemID+":"),
			anthropic.NewImageBlockBase64(ref.MediaType, base64.StdEncoding.EncodeToString(ref.Data)),
		)
	}
	if len(prefix) > 0 {
		prefix[len(prefix)-1].OfImage.CacheControl = anthropic.NewCacheControlEphemeralParam()
	} else {
		system[0].CacheControl = anthropic.NewCacheControlEphemeralParam()
	}

	// Volatile suffix: the till photo.
	content := append(prefix,
		anthropic.NewTextBlock("Photo taken at the till — identify this product:"),
		anthropic.NewImageBlockBase64(photoMediaType, base64.StdEncoding.EncodeToString(photo)),
	)

	ctx, cancel := context.WithTimeout(ctx, identifyTimeout)
	defer cancel()
	resp, err := s.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     s.model,
		MaxTokens: 1024,
		System:    system,
		OutputConfig: anthropic.OutputConfigParam{
			Format: anthropic.JSONOutputFormatParam{Schema: identifySchema},
		},
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(content...)},
	})
	if err != nil {
		return nil, err
	}
	if resp.StopReason == anthropic.StopReasonRefusal {
		return nil, fmt.Errorf("model declined the request")
	}
	var text string
	for _, block := range resp.Content {
		if b, ok := block.AsAny().(anthropic.TextBlock); ok {
			text = b.Text
			break
		}
	}
	if text == "" {
		return nil, fmt.Errorf("empty model response")
	}
	var out IdentifyResult
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		return nil, fmt.Errorf("parse model response: %w", err)
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
	return &out, nil
}
