//go:build wasip1

// Test guest for ADR-0172 §3 (ut-docs#6286): a tax plugin that declines every line (empty answer), like the UK plugin with its default empty mapping.
package main

import "github.com/universaltill/universal-till/sdk/plugin"

func main() {
	plugin.Run(plugin.Handlers{
		"*": func(plugin.Event) (any, error) { return nil, nil },
	})
}
