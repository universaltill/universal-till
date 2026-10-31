//go:build wasip1

// Test guest for the sell screen's camera identify seam (ADR-0121 §7
// catalog.identify, ut-docs#3873, ut-docs#4102): it ignores the photo and
// answers with a canned document — an optional text component and one
// "suggestions" component whose single add_to_basket candidate names a SKU.
// What it answers comes from its own plugin settings, so one compiled
// module serves every test:
//
//	sku    the SKU to suggest (default 2000010000012)
//	label  the suggestion's label (default "Identified item")
//	text   a text component shown above the suggestion (default none)
package main

import (
	"github.com/universaltill/universal-till/sdk/plugin"
)

const defaultSKU = "2000010000012"

func main() {
	plugin.Run(plugin.Handlers{"*": handle})
}

func setting(key, def string) string {
	if v, err := plugin.SettingsGet(key); err == nil && v != "" {
		return v
	}
	return def
}

func handle(plugin.Event) (any, error) {
	components := []any{}
	if text := setting("text", ""); text != "" {
		components = append(components, map[string]any{"type": "text", "text": map[string]any{"literal": text}})
	}
	components = append(components, map[string]any{
		"type": "suggestions",
		"items": []any{map[string]any{
			"label":  map[string]any{"literal": setting("label", "Identified item")},
			"effect": map[string]any{"add_to_basket": map[string]any{"sku": setting("sku", defaultSKU)}},
		}},
	})
	return map[string]any{"document": map[string]any{"version": 1, "components": components}}, nil
}
