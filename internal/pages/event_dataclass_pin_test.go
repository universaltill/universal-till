package pages

import (
	"testing"

	"github.com/universaltill/universal-till/internal/plugins"
)

// The events these pages raise to plugins must have a data-class set in
// core's registry (ADR-0153 amendment 2026-10-15, ut-docs#4147), or an
// ai.agent plugin is refused them; this pins the page constants to the
// registry's string keys.
func TestPageEventsHaveDataClasses(t *testing.T) {
	for _, ev := range []string{
		pluginViewAskEvent,
		pluginActionAskEvent,
		identifyEvent,
		identifyConfirmedEvent,
		agentActionResultEvent,
	} {
		if _, ok := plugins.EventDataClasses(ev); !ok {
			t.Errorf("%q has no entry in plugins.EventDataClasses", ev)
		}
	}
}
