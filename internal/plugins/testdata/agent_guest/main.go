//go:build wasip1

// Test guest for e2e/tests/plugin-confirm-5466.spec.ts (ut-docs#5466): an
// ai.agent plugin whose ui.view.ask answer proposes a catalogue item
// (catalog.item.create.v1) and returns a view holding core's `confirm`
// component for that proposal. It needs an auth-on till and a signed-in
// operator (ut-docs#4168). Installed by e2e/seed_agent_plugin; generic
// names only.
package main

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/universaltill/universal-till/sdk/plugin"
)

const (
	defaultName = "E2E agent item"
	priceMinor  = 250
)

type payload struct {
	View   string            `json:"view"`
	Params map[string]string `json:"params"`
}

func main() {
	plugin.Run(plugin.Handlers{"*": handle})
}

func handle(ev plugin.Event) (any, error) {
	var p payload
	if err := ev.Decode(&p); err != nil {
		return nil, err
	}
	if ev.Type != "ui.view.ask" || p.View != "e2eagent.confirm" {
		return nil, fmt.Errorf("unhandled %s %s", ev.Type, p.View)
	}
	name := strings.TrimSpace(p.Params["name"])
	if name == "" || utf8.RuneCountInString(name) > 60 {
		name = defaultName
	}
	// No barcode: a repeated run never hits the duplicate-barcode refusal.
	id, err := plugin.ActionPropose("catalog.item.create.v1", map[string]any{
		"items": []any{map[string]any{"name": name, "price_minor": priceMinor}},
	})
	if err != nil {
		return doc(map[string]any{"type": "notice", "level": "error",
			"text": map[string]string{"literal": "propose failed: " + err.Error()}}), nil
	}
	return doc(map[string]any{"type": "confirm", "proposal": id}), nil
}

func doc(c ...any) map[string]any {
	return map[string]any{"document": map[string]any{
		"version":    1,
		"title":      map[string]string{"key": "e2eagent.confirm.title"},
		"components": c,
	}}
}
