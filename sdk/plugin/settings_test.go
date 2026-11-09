package plugin

import (
	"encoding/json"
	"testing"
)

// ADR-0121 S5 (ut-docs#4160): the typed settings-options ask.
func TestDispatchSettingsOptionsAsk(t *testing.T) {
	h := Handlers{SettingsOptionsAskEvent: func(e Event) (any, error) {
		var req SettingsOptionsRequest
		if err := e.Decode(&req); err != nil {
			return nil, err
		}
		if req.Key != "model" || req.Values["provider"] != "a" || req.RecordID != "" {
			t.Errorf("request = %+v", req)
		}
		return SettingsOptionsAnswer{Options: []SettingsOption{{Value: "m1", Label: "Model one"}}}, nil
	}}
	out, err := Dispatch(h, []byte(`{"id":"1","type":"ui.settings.options.ask","payload":{"key":"model","values":{"provider":"a"}}}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"options":[{"value":"m1","label":"Model one"}]}` {
		t.Errorf("answer = %s", out)
	}
}

func TestSettingsOptionsAnswer_NilIsEmptyList(t *testing.T) {
	b, err := json.Marshal(SettingsOptionsAnswer{})
	if err != nil || string(b) != `{"options":[]}` {
		t.Errorf("got %s, %v", b, err)
	}
}
