package plugin

import "encoding/json"

// Plugin settings asks (ADR-0121 S5, ut-docs#4160). A `choice` setting with
// "options_ask": true gets its dropdown entries from the plugin: declare
// `{"event": "ui.settings.options.ask", "action": "settings"}` in the
// manifest's hooks (and the events:receive permission), decode the ask into
// SettingsOptionsRequest and answer with SettingsOptionsAnswer. Contract:
// ut-docs reference/plugin-views.md, "Settings asks".

// SettingsOptionsAskEvent is the event type of the ask.
const SettingsOptionsAskEvent = "ui.settings.options.ask"

// SettingsOptionsRequest is the ask's payload. Values are the form's current
// non-secret settings, unsaved edits included, each exactly as settings_get
// would return it once saved (a switch is "true"/"false", a number, percent
// or amount its canonical integer text); a secret, list or map is never in
// it — read a saved secret with SettingsGet.
type SettingsOptionsRequest struct {
	Key      string            `json:"key"`
	RecordID string            `json:"record_id,omitempty"` // reserved for record-scoped lists (ut-docs#4158); empty today
	Values   map[string]string `json:"values"`
}

// SettingsOption is one entry of the dropdown. Value is what gets saved: 1 to
// 64 bytes of UTF-8, no comma, no control character, no surrounding space.
// Label is plain text, at most 128 bytes (empty shows the value); the till
// escapes it and never translates it.
type SettingsOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// SettingsOptionsAnswer is the answer: at most 32 options with distinct
// values. Any violation makes the till drop the whole answer and tell the
// operator the choices could not be loaded. A nil or empty Options (or
// returning no answer) is an empty list, not an error.
type SettingsOptionsAnswer struct {
	Options []SettingsOption `json:"options"`
}

// MarshalJSON writes a nil Options as [] rather than null.
func (a SettingsOptionsAnswer) MarshalJSON() ([]byte, error) {
	opts := a.Options
	if opts == nil {
		opts = []SettingsOption{}
	}
	return json.Marshal(struct {
		Options []SettingsOption `json:"options"`
	}{opts})
}
