//go:build !wasip1

package channel

import (
	"encoding/json"
	"testing"
)

// ut-docs#7084: the product doorbell event and the webhook registration ask
// are additive in contract v1. The strings are the wire ABI the cloud host
// matches, so they are pinned here.
func TestWebhookRegisterABI(t *testing.T) {
	if EventProductChanged != "product_changed" {
		t.Fatalf("event %q", EventProductChanged)
	}
	if AskWebhookRegister != "channel.webhook.register.ask" {
		t.Fatalf("ask %q", AskWebhookRegister)
	}
	if WebhookModeEnsure != "ensure" || WebhookModeRemove != "remove" {
		t.Fatal("modes")
	}
	for s, want := range map[string]string{TopicRegistered: "registered", TopicAlready: "already", TopicUpdated: "updated", TopicRemoved: "removed", TopicUnsupported: "unsupported", TopicFailed: "failed"} {
		if s != want {
			t.Fatalf("state %q want %q", s, want)
		}
	}
}

func TestWebhookRegisterJSONShape(t *testing.T) {
	in, _ := json.Marshal(WebhookRegisterInput{CallbackURL: "https://cloud.example/hooks/channel/p/r", Topics: []string{"products/update"}, Mode: WebhookModeEnsure})
	if string(in) != `{"callback_url":"https://cloud.example/hooks/channel/p/r","topics":["products/update"],"mode":"ensure"}` {
		t.Fatalf("input %s", in)
	}
	out, _ := json.Marshal(WebhookRegisterResult{Topics: map[string]TopicState{
		"products/update": {State: TopicRegistered},
		"shop/redact":     {State: TopicUnsupported},
		"products/create": {State: TopicFailed, Code: "scope_missing"},
	}})
	want := `{"topics":{"products/create":{"state":"failed","code":"scope_missing"},"products/update":{"state":"registered"},"shop/redact":{"state":"unsupported"}}}`
	if string(out) != want {
		t.Fatalf("result %s", out)
	}
}

// The ask reaches a guest handler through Dispatch like every other ask.
func TestWebhookRegisterDispatches(t *testing.T) {
	UseFakeHost(t, NewFakeHost())
	got := ""
	h := Handlers{AskWebhookRegister: func(ev Event) (any, error) {
		var in WebhookRegisterInput
		if err := ev.Decode(&in); err != nil {
			return nil, err
		}
		got = in.Mode
		return WebhookRegisterResult{Topics: map[string]TopicState{"products/update": {State: TopicAlready}}}, nil
	}}
	out, err := Dispatch(h, []byte(`{"id":"e1","type":"channel.webhook.register.ask","payload":{"callback_url":"https://x.example/hooks/channel/a","topics":["products/update"],"mode":"ensure"}}`))
	if err != nil || got != "ensure" {
		t.Fatalf("%v %q", err, got)
	}
	if string(out) != `{"topics":{"products/update":{"state":"already"}}}` {
		t.Fatalf("%s", out)
	}
}
