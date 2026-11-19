//go:build wasip1

// Test guest for ADR-0172 §3 (ut-docs#6286): a tax plugin that exits non-zero on every ask (a trap).
package main

import (
	"errors"

	"github.com/universaltill/universal-till/sdk/plugin"
)

func main() {
	plugin.Run(plugin.Handlers{
		"*": func(plugin.Event) (any, error) { return nil, errors.New("boom") },
	})
}
