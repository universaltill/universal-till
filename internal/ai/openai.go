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

// openaiProvider is the second OPTIONAL hosted backend (UT_AI_PROVIDER=openai,
// ADR-0085 extended by ut-docs#1791) for shops that explicitly choose a paid
// API. Unlike claudeProvider it implements BOTH provider (identify) and
// asker (ask) — OpenAI's Chat Completions API supports both vision input and
// function/tool calling on the same model, so a shop on this provider gets a
// working "Ask your till" from day one. The default posture is still the
// self-hosted ollamaProvider — see the package comment.
//
// Talks to the real REST API via plain net/http + encoding/json rather than
// an SDK: no OpenAI Go SDK dependency exists in go.mod, and adding one is out
// of scope for this card (mirrors ollama_ask.go's raw-HTTP style, not
// claude.go's SDK style).
type openaiProvider struct {
	apiKey      string
	visionModel string // camera identify
	askModel    string // tool-capable model for "Ask your till"
	baseURL     string // https://api.openai.com/v1, overridable in tests
	client      *http.Client
}

func newOpenAIProvider(apiKey, visionModel, askModel string) *openaiProvider {
	return &openaiProvider{
		apiKey:      apiKey,
		visionModel: visionModel,
		askModel:    askModel,
		baseURL:     "https://api.openai.com/v1",
		client:      &http.Client{}, // per-call deadline comes from the caller's ctx
	}
}

// openAIToolCall is OpenAI's real wire shape for a requested tool call.
// LOAD-BEARING DETAIL: Arguments is a JSON-encoded STRING, not an object
// (Ollama's shape uses an object) — get this wrong and every tool call
// silently fails to decode.
type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// openAIMessage covers every message role this provider sends or receives:
// system/user (Content only), assistant (Content and/or ToolCalls, which
// must be echoed back verbatim on the next round per the API contract), and
// tool (ToolCallID + Content = the tool's result text).
type openAIMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	Refusal    string           `json:"refusal,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

// dataURL builds the data: URL form of an image_url part OpenAI's multimodal
// content array expects.
func dataURL(mediaType string, data []byte) string {
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data)
}

func (p *openaiProvider) identify(ctx context.Context, photo []byte, photoMediaType string, items []CatalogItem, refs []RefImage) (*IdentifyResult, error) {
	prompt, err := identifyPrompt(items)
	if err != nil {
		return nil, err
	}

	content := make([]map[string]any, 0, len(refs)*2+2)
	for _, ref := range refs {
		content = append(content,
			map[string]any{"type": "text", "text": "Reference image for item " + ref.ItemID + ":"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": dataURL(ref.MediaType, ref.Data)}},
		)
	}
	content = append(content,
		map[string]any{"type": "text", "text": "Photo taken at the till — identify this product:"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": dataURL(photoMediaType, photo)}},
	)

	msg, err := p.chatCompletion(ctx, map[string]any{
		"model": p.visionModel,
		"messages": []map[string]any{
			{"role": "system", "content": prompt},
			{"role": "user", "content": content},
		},
		"response_format": map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "identify_result",
				"schema": identifySchema,
				"strict": true,
			},
		},
	})
	if err != nil {
		return nil, err
	}
	if msg.Refusal != "" {
		return nil, fmt.Errorf("model declined the request")
	}
	if msg.Content == "" {
		return nil, fmt.Errorf("empty model response")
	}
	var out IdentifyResult
	if err := json.Unmarshal([]byte(msg.Content), &out); err != nil {
		return nil, fmt.Errorf("parse model response: %w", err)
	}
	return &out, nil
}

// ask runs OpenAI's tool-use loop (POST /chat/completions with "tools"),
// bounded by the shared maxToolRounds (internal/ai/ollama_ask.go) — a lost
// model must not spin forever, same guardrail as the self-hosted loop.
func (p *openaiProvider) ask(ctx context.Context, system, question string, tools []AskTool) (string, error) {
	toolDefs := make([]map[string]any, 0, len(tools))
	byName := make(map[string]AskTool, len(tools))
	for _, t := range tools {
		byName[t.Name] = t
		toolDefs = append(toolDefs, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.Params,
			},
		})
	}
	messages := []openAIMessage{
		{Role: "system", Content: system},
		{Role: "user", Content: question},
	}
	for range maxToolRounds {
		payload := map[string]any{
			"model":    p.askModel,
			"messages": messages,
		}
		if len(toolDefs) > 0 {
			payload["tools"] = toolDefs
		}
		msg, err := p.chatCompletion(ctx, payload)
		if err != nil {
			return "", err
		}
		if msg.Refusal != "" {
			return "", fmt.Errorf("model declined the request")
		}
		messages = append(messages, msg)
		if len(msg.ToolCalls) == 0 {
			if strings.TrimSpace(msg.Content) == "" {
				return "", fmt.Errorf("empty model response")
			}
			return msg.Content, nil
		}
		for _, tc := range msg.ToolCalls {
			messages = append(messages, openAIMessage{
				Role:       "tool",
				ToolCallID: tc.ID,
				Content:    runOpenAIToolCall(ctx, byName, tc),
			})
		}
	}
	return "", fmt.Errorf("model did not answer within %d tool rounds", maxToolRounds)
}

// runOpenAIToolCall adapts OpenAI's tool-call shape (arguments as a JSON
// string) to ollamaToolCall (arguments as an object) so it can reuse
// runAskTool's dispatch logic unchanged.
func runOpenAIToolCall(ctx context.Context, byName map[string]AskTool, tc openAIToolCall) string {
	var args map[string]any
	if s := strings.TrimSpace(tc.Function.Arguments); s != "" {
		if err := json.Unmarshal([]byte(s), &args); err != nil {
			return "error: invalid tool arguments: " + err.Error()
		}
	}
	var adapted ollamaToolCall
	adapted.Function.Name = tc.Function.Name
	adapted.Function.Arguments = args
	return runAskTool(ctx, byName, adapted)
}

// chatCompletion is one non-streaming POST /chat/completions exchange,
// shared by identify and the ask loop.
func (p *openaiProvider) chatCompletion(ctx context.Context, payload map[string]any) (openAIMessage, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return openAIMessage{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return openAIMessage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	resp, err := p.client.Do(req)
	if err != nil {
		return openAIMessage{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return openAIMessage{}, err
	}
	if resp.StatusCode != http.StatusOK {
		// 401/403 bodies from OpenAI echo a masked fragment of the caller's
		// own API key back (e.g. "Incorrect API key provided: sk-proj-***abcd") —
		// this error is logged upstream (internal/pages/ai_api.go), so a
		// verbatim body here would put that fragment in the till's logs.
		// Every other status is safe to include as-is for diagnosis.
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return openAIMessage{}, fmt.Errorf("openai %s: authentication failed (key rejected)", resp.Status)
		}
		return openAIMessage{}, fmt.Errorf("openai %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	var envelope struct {
		Choices []struct {
			Message openAIMessage `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return openAIMessage{}, fmt.Errorf("parse openai response: %w", err)
	}
	if len(envelope.Choices) == 0 {
		return openAIMessage{}, fmt.Errorf("empty model response")
	}
	return envelope.Choices[0].Message, nil
}
