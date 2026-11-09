//go:build wasip1

// Test guest for e2e/tests/plugin-settings-options-4160.spec.ts (ut-docs#4160):
// answers ui.settings.options.ask for the "model" setting from the unsaved
// "provider" value. Installed by e2e/seed_settings_plugin; generic names only.
//
//	provider a -> two options, one whose label is markup (must show as text)
//	provider b -> one option
//	provider c -> 33 options (over the 32 cap: the whole answer is refused)
//	else       -> no options
package main

import (
	"fmt"

	"github.com/universaltill/universal-till/sdk/plugin"
)

func main() {
	plugin.Run(plugin.Handlers{plugin.SettingsOptionsAskEvent: handle})
}

func handle(e plugin.Event) (any, error) {
	var req plugin.SettingsOptionsRequest
	if err := e.Decode(&req); err != nil {
		return nil, err
	}
	if req.Key != "model" {
		return nil, nil
	}
	switch req.Values["provider"] {
	case "a":
		return plugin.SettingsOptionsAnswer{Options: []plugin.SettingsOption{
			{Value: "alpha-small", Label: "Alpha small"},
			{Value: "alpha-big", Label: "<b>Big</b> alpha"},
		}}, nil
	case "b":
		return plugin.SettingsOptionsAnswer{Options: []plugin.SettingsOption{{Value: "beta-only", Label: "Beta only"}}}, nil
	case "c":
		var opts []plugin.SettingsOption
		for i := 0; i < 33; i++ {
			opts = append(opts, plugin.SettingsOption{Value: fmt.Sprintf("gamma-%d", i), Label: fmt.Sprintf("Gamma %d", i)})
		}
		return plugin.SettingsOptionsAnswer{Options: opts}, nil
	}
	return plugin.SettingsOptionsAnswer{}, nil
}
