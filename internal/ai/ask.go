package ai

import (
	"context"
	"fmt"
	"time"
)

// AskTool is one read-only, repo-backed capability the model may call while
// answering. Guardrail by construction: pages only wire read methods, so the
// model cannot mutate anything no matter what it asks for.
type AskTool struct {
	Name        string
	Description string
	Params      map[string]any // JSON schema for the arguments object
	Run         func(ctx context.Context, args map[string]any) (any, error)
}

// ShopContext grounds the model's answers in this till's reality.
type ShopContext struct {
	StoreName        string
	CurrencyCode     string
	CurrencyDecimals int
	Locale           string
}

// asker is the optional provider capability behind "Ask your till"
// (ai-integration.md §a). Only providers that can run a tool-use loop
// implement it; the UI hides the panel otherwise.
type asker interface {
	ask(ctx context.Context, system, question string, tools []AskTool) (string, error)
}

// CanAsk reports whether the configured backend supports the tool-use loop.
func (s *Service) CanAsk() bool {
	if !s.Enabled() {
		return false
	}
	_, ok := s.p.(asker)
	return ok
}

const askTimeout = 120 * time.Second // several tool rounds on modest hardware

func askSystemPrompt(shop ShopContext) string {
	return fmt.Sprintf(
		"You are the built-in assistant of a point-of-sale till in the shop %q. "+
			"You answer the shop manager's questions about sales, stock and till activity "+
			"using ONLY the provided tools — never invent figures. "+
			"All monetary amounts in tool results are integers in minor units of %s "+
			"(%d decimal places); convert them to normal readable amounts in your answer. "+
			"Today is %s. Be concise and practical: a short direct answer first, "+
			"then at most a few supporting numbers. If the data genuinely cannot answer "+
			"the question, say so plainly.",
		shop.StoreName, shop.CurrencyCode, shop.CurrencyDecimals,
		time.Now().Format("Monday 2006-01-02"))
}

// Ask answers a manager's natural-language question via the provider's
// tool-use loop. Callers gate on CanAsk and treat errors as "unavailable".
func (s *Service) Ask(ctx context.Context, question string, shop ShopContext, tools []AskTool) (string, error) {
	a, ok := s.p.(asker)
	if !ok {
		return "", fmt.Errorf("ask not supported by this ai provider")
	}
	if question == "" {
		return "", fmt.Errorf("question required")
	}
	ctx, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()
	return a.ask(ctx, askSystemPrompt(shop), question, tools)
}
