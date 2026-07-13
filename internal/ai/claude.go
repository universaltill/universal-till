package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// claudeProvider is the OPTIONAL hosted backend (UT_AI_PROVIDER=claude) for
// shops that explicitly choose a paid API. The default posture is the
// self-hosted ollamaProvider — see the package comment.
type claudeProvider struct {
	client anthropic.Client
	model  anthropic.Model
}

func newClaudeProvider(apiKey, model string) *claudeProvider {
	return &claudeProvider{
		client: anthropic.NewClient(option.WithAPIKey(apiKey)),
		model:  anthropic.Model(model),
	}
}

func (p *claudeProvider) identify(ctx context.Context, photo []byte, photoMediaType string, items []CatalogItem, refs []RefImage) (*IdentifyResult, error) {
	prompt, err := identifyPrompt(items)
	if err != nil {
		return nil, err
	}
	system := []anthropic.TextBlockParam{{Text: prompt}}

	// Stable prefix: labelled reference images, cache breakpoint on the last
	// one (or on the system block when there are no references) so repeated
	// identifications re-read the catalog context at ~10% of the cost.
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

	content := append(prefix,
		anthropic.NewTextBlock("Photo taken at the till — identify this product:"),
		anthropic.NewImageBlockBase64(photoMediaType, base64.StdEncoding.EncodeToString(photo)),
	)

	resp, err := p.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     p.model,
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
	return &out, nil
}
