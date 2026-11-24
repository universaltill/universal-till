package pluginsecretapply

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/directivekey"
)

// vector is the shared cross-repo vector (ut-docs
// reference/contracts/plugin-secret-v1.json, byte copy in testdata).
type vector struct {
	RecipientSK     string `json:"recipient_sk"`
	KID             string `json:"kid"`
	StoreExternalID string `json:"store_external_id"`
	DirectiveID     string `json:"directive_id"`
	Type            string `json:"type"`
	PluginID        string `json:"plugin_id"`
	SettingKey      string `json:"setting_key"`
	Plaintext       string `json:"plaintext"`
	Info            string `json:"info"`
	AAD             string `json:"aad"`
	SecretSealed    string `json:"secret_sealed"`
}

func loadVector(t *testing.T) vector {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "plugin-secret-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v vector
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func keysFor(t *testing.T, skHex string) *directivekey.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secrets", "directive-x25519.key")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	sk, _ := hex.DecodeString(skHex)
	if err := os.WriteFile(path, sk, 0o600); err != nil {
		t.Fatal(err)
	}
	return directivekey.NewAt(path)
}

type fakeSettings struct {
	vals    map[string]string
	saveErr error
	saved   [][]Write
}

func (f *fakeSettings) Get(_ context.Context, plugin, key string) (string, bool, error) {
	v, ok := f.vals[plugin+"/"+key]
	return v, ok, nil
}

func (f *fakeSettings) Save(_ context.Context, plugin string, w []Write) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saved = append(f.saved, w)
	for _, x := range w {
		f.vals[plugin+"/"+x.Key] = x.ValueJSON
	}
	return nil
}

type fixture struct {
	v   vector
	set *fakeSettings
	a   *Applier
	d   Directive
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	v := loadVector(t)
	set := &fakeSettings{vals: map[string]string{}}
	f := &fixture{v: v, set: set}
	f.a = &Applier{
		Keys:     keysFor(t, v.RecipientSK),
		Settings: set,
		StoreID:  func() string { return v.StoreExternalID },
		DeclaresSecret: func(context.Context, string, string) (bool, bool, error) {
			return true, true, nil // installed, declared type secret
		},
	}
	f.d = Directive{DirectiveID: v.DirectiveID, Type: v.Type, PluginID: v.PluginID,
		SettingKey: v.SettingKey, Ver: 3, SecretSealed: v.SecretSealed}
	return f
}

func TestApply_SharedVector_WritesKeyCodeAndVersion(t *testing.T) {
	f := newFixture(t)
	msg, err := f.a.Apply(context.Background(), f.d)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if msg != "applied" {
		t.Fatalf("message = %q, want applied", msg)
	}
	if got := f.set.vals["sumup/sumup_api_key"]; got != `"sumup-fake-test-key-0000000000000000"` {
		t.Fatalf("key setting = %q", got)
	}
	if got := f.set.vals["sumup/sumup_merchant_code"]; got != `"MTEST123"` {
		t.Fatalf("merchant code = %q", got)
	}
	if got := f.set.vals["sumup/sumup_key_version"]; got != "3" {
		t.Fatalf("version = %q, want 3", got)
	}
	if len(f.set.saved) != 1 {
		t.Fatalf("want one batch write, got %d", len(f.set.saved))
	}
	// The key row is written declared-secret so it is sealed at rest.
	for _, w := range f.set.saved[0] {
		if w.Key == "sumup_api_key" && !w.DeclaredSecret {
			t.Fatal("key must be written with DeclaredSecret")
		}
	}
}

func TestApply_RefusalsCarryReasonCodeOnly(t *testing.T) {
	canary := "sumup-fake-test-key-0000000000000000"
	cases := []struct {
		name   string
		mutate func(f *fixture)
		want   string
	}{
		{"wrong plugin", func(f *fixture) { f.d.PluginID = "other" }, "not_allowed"},
		{"wrong key", func(f *fixture) { f.d.SettingKey = "sumup_merchant_code" }, "not_allowed"},
		{"not installed", func(f *fixture) {
			f.a.DeclaresSecret = func(context.Context, string, string) (bool, bool, error) { return false, false, nil }
		}, "plugin_not_installed"},
		{"not declared secret", func(f *fixture) {
			f.a.DeclaresSecret = func(context.Context, string, string) (bool, bool, error) { return false, true, nil }
		}, "setting_not_secret"},
		{"no key", func(f *fixture) { f.a.Keys = directivekey.NewAt(filepath.Join(t.TempDir(), "none")) }, "no_till_key"},
		{"kid mismatch", func(f *fixture) {
			f.d.SecretSealed = strings.Replace(f.d.SecretSealed, "v1."+f.v.KID, "v1.0000000000000000", 1)
		}, "kid_mismatch"},
		{"wrong type", func(f *fixture) { f.d.Type = "set_user_pin" }, "not_allowed"},
		{"aad directive changed", func(f *fixture) { f.d.DirectiveID = "other" }, "open_failed"},
		{"malformed", func(f *fixture) { f.d.SecretSealed = "garbage" }, "open_failed"},
		{"ver mismatch with plaintext", func(f *fixture) { f.d.Ver = 4 }, "open_failed"},
		{"stale version", func(f *fixture) { f.set.vals["sumup/sumup_key_version"] = "3" }, "stale_version"},
		{"older version", func(f *fixture) { f.set.vals["sumup/sumup_key_version"] = "9" }, "stale_version"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			c.mutate(f)
			_, err := f.a.Apply(context.Background(), f.d)
			if err == nil || err.Error() != c.want {
				t.Fatalf("got %v, want %q", err, c.want)
			}
			if errors.Is(err, ErrRetryable) {
				t.Fatalf("%s must be terminal", c.want)
			}
			if len(f.set.saved) != 0 {
				t.Fatal("a refusal must not write anything")
			}
			if strings.Contains(err.Error(), canary) {
				t.Fatal("error leaks the key")
			}
		})
	}
}

func TestApply_WriteFailureIsRetryableWriteFailed(t *testing.T) {
	f := newFixture(t)
	f.set.saveErr = errors.New("database is locked: sumup-fake-test-key-0000000000000000")
	_, err := f.a.Apply(context.Background(), f.d)
	if err == nil || err.Error() != "write_failed" || !errors.Is(err, ErrRetryable) {
		t.Fatalf("got %v, want retryable write_failed", err)
	}
}

func TestApply_ManifestUnreadableIsRetryable(t *testing.T) {
	f := newFixture(t)
	f.a.DeclaresSecret = func(context.Context, string, string) (bool, bool, error) {
		return false, false, errors.New("boom")
	}
	_, err := f.a.Apply(context.Background(), f.d)
	if err == nil || err.Error() != "write_failed" || !errors.Is(err, ErrRetryable) {
		t.Fatalf("got %v", err)
	}
}

func TestSecretStore_PlaintextShapes(t *testing.T) {
	cases := map[string]string{
		"bad json":    "not json",
		"wrong v":     `{"v":2,"ver":3,"value":"k","aux":{}}`,
		"unknown aux": `{"v":1,"ver":3,"value":"k","aux":{"evil":"x"}}`,
		"huge value":  `{"v":1,"ver":3,"value":"` + strings.Repeat("a", 2000) + `","aux":{}}`,
		"ctrl char":   `{"v":1,"ver":3,"value":"a\nb","aux":{}}`,
	}
	for name, pt := range cases {
		if _, err := parsePlaintext([]byte(pt)); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	c, err := parsePlaintext([]byte(`{"v":1,"ver":4,"value":"","aux":{"sumup_merchant_code":""}}`))
	if err != nil || c.Value != "" || c.Ver != 4 {
		t.Fatalf("clear: %+v %v", c, err)
	}
}
