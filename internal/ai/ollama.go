package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ollamaProvider talks to a self-hosted Ollama server (/api/chat) running an
// open vision model — no account, no metering, nothing leaves the shop.
// Reference images are deliberately NOT sent: the small open vision models
// this targets lose accuracy when dozens of images share one request, so the
// photo + catalog text does the matching and the ai_ref corpus is kept for
// training a custom per-shop matcher later.
type ollamaProvider struct {
	endpoint string
	model    string
	client   *http.Client
}

func newOllamaProvider(endpoint, model string) *ollamaProvider {
	return &ollamaProvider{
		endpoint: strings.TrimSuffix(endpoint, "/"),
		model:    model,
		client:   &http.Client{}, // per-call deadline comes from the caller's ctx
	}
}

func (p *ollamaProvider) identify(ctx context.Context, photo []byte, _ string, items []CatalogItem, _ []RefImage) (*IdentifyResult, error) {
	prompt, err := identifyPrompt(items)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]any{
		"model":  p.model,
		"stream": false,
		"format": identifySchema,
		"messages": []map[string]any{{
			"role":    "user",
			"content": prompt + "\n\nIdentify the product in the attached photo.",
			"images":  []string{base64.StdEncoding.EncodeToString(photo)},
		}},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	var envelope struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("parse ollama response: %w", err)
	}
	if envelope.Message.Content == "" {
		return nil, fmt.Errorf("empty model response")
	}
	var out IdentifyResult
	if err := json.Unmarshal([]byte(envelope.Message.Content), &out); err != nil {
		return nil, fmt.Errorf("parse model response: %w", err)
	}
	return &out, nil
}
