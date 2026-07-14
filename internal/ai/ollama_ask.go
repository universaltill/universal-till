package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// maxToolRounds bounds the loop; a lost model must not spin forever.
const maxToolRounds = 6

// ollamaMessage keeps the assistant's raw tool_calls so they can be echoed
// back verbatim on the next round, as the chat contract expects.
type ollamaMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
	ToolName  string           `json:"tool_name,omitempty"`
}

type ollamaToolCall struct {
	Function struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	} `json:"function"`
}

// ask runs the standard Ollama tool-use loop (/api/chat with "tools"): the
// model either answers or requests tool calls; results go back as role
// "tool" messages until it answers. Requires a tool-capable text model
// (llama3.2, qwen2.5, …) — vision models generally don't support tools,
// hence the separate ask model.
func (p *ollamaProvider) ask(ctx context.Context, system, question string, tools []AskTool) (string, error) {
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
	messages := []ollamaMessage{
		{Role: "system", Content: system},
		{Role: "user", Content: question},
	}
	for range maxToolRounds {
		msg, err := p.chat(ctx, p.askModel, messages, toolDefs)
		if err != nil {
			return "", err
		}
		messages = append(messages, msg)
		if len(msg.ToolCalls) == 0 {
			if strings.TrimSpace(msg.Content) == "" {
				return "", fmt.Errorf("empty model response")
			}
			return msg.Content, nil
		}
		for _, tc := range msg.ToolCalls {
			messages = append(messages, ollamaMessage{
				Role:     "tool",
				ToolName: tc.Function.Name,
				Content:  runAskTool(ctx, byName, tc),
			})
		}
	}
	return "", fmt.Errorf("model did not answer within %d tool rounds", maxToolRounds)
}

// runAskTool executes one requested tool; errors go back to the model as
// text so it can recover (ask differently or apologise) instead of aborting.
func runAskTool(ctx context.Context, byName map[string]AskTool, tc ollamaToolCall) string {
	tool, ok := byName[tc.Function.Name]
	if !ok {
		return fmt.Sprintf("error: unknown tool %q", tc.Function.Name)
	}
	result, err := tool.Run(ctx, tc.Function.Arguments)
	if err != nil {
		return "error: " + err.Error()
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return "error: " + err.Error()
	}
	return string(raw)
}

// chat is one non-streaming /api/chat exchange.
func (p *ollamaProvider) chat(ctx context.Context, model string, messages []ollamaMessage, tools []map[string]any) (ollamaMessage, error) {
	payload := map[string]any{
		"model":    model,
		"stream":   false,
		"messages": messages,
	}
	if len(tools) > 0 {
		payload["tools"] = tools
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return ollamaMessage{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return ollamaMessage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return ollamaMessage{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ollamaMessage{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return ollamaMessage{}, fmt.Errorf("ollama %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	var envelope struct {
		Message ollamaMessage `json:"message"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return ollamaMessage{}, fmt.Errorf("parse ollama response: %w", err)
	}
	return envelope.Message, nil
}
