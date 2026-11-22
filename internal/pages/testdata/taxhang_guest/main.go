//go:build wasip1

// Test guest for ADR-0172 §3 (ut-docs#6286): a tax plugin that never answers (a CPU busy loop), so the first-load check must time it out.
package main

import "github.com/universaltill/universal-till/sdk/plugin"

func main() {
	plugin.Run(plugin.Handlers{
		"*": func(plugin.Event) (any, error) {
			n := 0
			for {
				n++
			}
		},
	})
}
