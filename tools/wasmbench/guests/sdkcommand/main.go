//go:build wasip1

// SDK command guest: the command guest's work through the Go guest SDK's
// event loop (sdk/plugin, ut-docs#3951), so the bench shows what the SDK adds
// per event against the hand-rolled command guest.
package main

import (
	"encoding/json"

	"github.com/universaltill/universal-till/sdk/plugin"
	"github.com/universaltill/universal-till/tools/wasmbench/guests/answer"
)

func main() {
	plugin.Run(plugin.Handlers{
		"charge.policy.ask": func(plugin.Event) (any, error) {
			out, err := answer.Policy()
			return json.RawMessage(out), err
		},
	})
}
