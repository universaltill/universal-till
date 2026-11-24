package cloudsync

import (
	"context"
	"errors"
	"testing"
)

// set_plugin_secret (ADR-0213 Decision 5, ut-docs#6960): main-till only; this
// package decodes the envelope and hands the opaque sealed string to an
// injected hook. Which plugin setting it lands in is the hook's business.

func TestSetPluginSecretIsMainTillOnly(t *testing.T) {
	if !mainTillOnlyTypes["set_plugin_secret"] {
		t.Fatal("set_plugin_secret must be main-till only")
	}
	if catalogTypes["set_plugin_secret"] {
		t.Fatal("set_plugin_secret must not be a catalog type")
	}
}

func TestApplySetPluginSecret_NilHookUnsupported(t *testing.T) {
	status, msg := apply(context.Background(), directive{ID: "d", Type: "set_plugin_secret", Payload: map[string]any{}}, Hooks{})
	if status != "failed" || msg != "set_plugin_secret is not supported on this till" {
		t.Fatalf("%q %q", status, msg)
	}
}

func TestApplySetPluginSecret_Decode(t *testing.T) {
	var got PluginSecretDirective
	calls := 0
	hooks := Hooks{SetPluginSecret: func(_ context.Context, d PluginSecretDirective) (string, error) {
		calls++
		got = d
		return "applied", nil
	}}
	good := func() map[string]any {
		return map[string]any{"plugin_id": "p", "setting_key": "k", "ver": 3.0, "secret_sealed": "v1.a.b.c"}
	}
	for name, mut := range map[string]func(m map[string]any){
		"no plugin":      func(m map[string]any) { delete(m, "plugin_id") },
		"no setting":     func(m map[string]any) { delete(m, "setting_key") },
		"no sealed":      func(m map[string]any) { delete(m, "secret_sealed") },
		"empty sealed":   func(m map[string]any) { m["secret_sealed"] = "" },
		"sealed number":  func(m map[string]any) { m["secret_sealed"] = 4.0 },
		"no ver":         func(m map[string]any) { delete(m, "ver") },
		"zero ver":       func(m map[string]any) { m["ver"] = 0.0 },
		"fractional ver": func(m map[string]any) { m["ver"] = 1.5 },
	} {
		m := good()
		mut(m)
		status, msg := apply(context.Background(), directive{ID: "d", Type: "set_plugin_secret", Payload: m}, hooks)
		if status != "failed" || msg == "" {
			t.Errorf("%s: %q %q, want failed", name, status, msg)
		}
	}
	if calls != 0 {
		t.Fatalf("hook ran %d times for bad payloads", calls)
	}
	status, _ := apply(context.Background(), directive{ID: "d1", Type: "set_plugin_secret", Payload: good()}, hooks)
	if status != "applied" || calls != 1 {
		t.Fatalf("status %q calls %d", status, calls)
	}
	want := PluginSecretDirective{DirectiveID: "d1", Type: "set_plugin_secret", PluginID: "p", SettingKey: "k", Ver: 3, SecretSealed: "v1.a.b.c"}
	if got != want {
		t.Fatalf("decoded %+v, want %+v", got, want)
	}
}

func TestApplySetPluginSecret_HookErrors(t *testing.T) {
	mk := func(err error) Hooks {
		return Hooks{SetPluginSecret: func(context.Context, PluginSecretDirective) (string, error) { return "", err }}
	}
	p := map[string]any{"plugin_id": "p", "setting_key": "k", "ver": 1.0, "secret_sealed": "v1.a.b.c"}
	status, msg := apply(context.Background(), directive{ID: "d", Type: "set_plugin_secret", Payload: p}, mk(errors.New("kid_mismatch")))
	if status != "failed" || msg != "kid_mismatch" {
		t.Fatalf("terminal: %q %q", status, msg)
	}
	status, msg = apply(context.Background(), directive{ID: "d", Type: "set_plugin_secret", Payload: p}, mk(ErrPluginSecretRetry))
	if status != statusRetry || msg != "write_failed" {
		t.Fatalf("retry: %q %q (stays pending, nothing posted)", status, msg)
	}
}
