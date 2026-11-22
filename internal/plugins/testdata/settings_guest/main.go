//go:build wasip1

// Test guest for e2e/tests/plugin-settings-options-4160.spec.ts (ut-docs#4160):
// answers ui.settings.options.ask for the "model" setting from the unsaved
// "provider" value. Installed by e2e/seed_settings_plugin; generic names only.
//
//	provider a -> two options, one whose label is markup (must show as text)
//	provider b -> one option
//	provider c -> 33 options (over the 32 cap: the whole answer is refused)
//	else       -> no options
//
// It also answers ui.settings.test.ask (ut-docs#5961, plugin-settings-test-5961.spec.ts)
// from the SAVED "mode" setting, read with SettingsGet (the ask carries no value):
//
//	ok    -> {ok:true}
//	okmsg -> {ok:true, message:"e2e.test.ok"} (a key of the plugin's own bundle)
//	bad   -> {ok:false, message:"e2e.test.bad", field:"provider"}
//	slow  -> blocks past the till's 10 s bound
//	else  -> no answer ("couldn't run")
package main

import (
	"fmt"
	"time"

	"github.com/universaltill/universal-till/sdk/plugin"
)

func main() {
	plugin.Run(plugin.Handlers{plugin.SettingsOptionsAskEvent: handle, plugin.SettingsTestAskEvent: handleTest})
}

func handleTest(e plugin.Event) (any, error) {
	var req plugin.SettingsTestRequest
	if err := e.Decode(&req); err != nil {
		return nil, err
	}
	if req.Test != "conn" {
		return nil, nil
	}
	mode, err := plugin.SettingsGet("mode")
	if err != nil {
		return nil, err
	}
	switch mode {
	case "ok":
		return plugin.SettingsTestAnswer{OK: true}, nil
	case "okmsg":
		return plugin.SettingsTestAnswer{OK: true, Message: "e2e.test.ok"}, nil
	case "bad":
		return plugin.SettingsTestAnswer{OK: false, Message: "e2e.test.bad", Field: "provider"}, nil
	case "slow":
		time.Sleep(11 * time.Second)
		return plugin.SettingsTestAnswer{OK: true}, nil
	}
	return nil, nil
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
