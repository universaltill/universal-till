//go:build wasip1

// Test guest for ADR-0172 §3 (ut-docs#6286): a tax plugin that answers a
// rate outside any sane range.
package main

import "github.com/universaltill/universal-till/sdk/plugin"

func main() {
	plugin.Run(plugin.Handlers{
		"*": func(plugin.Event) (any, error) { return []byte(`{"rate_bp":-5}`), nil },
	})
}
