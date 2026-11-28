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
	"github.com/universaltill/universal-till/internal/plugins"
)

// vectors is the shared cross-repo file (ut-docs
// reference/contracts/plugin-secret-v1.json, BYTE COPY in testdata): the v1 vector at the top level and the
// wire v2 cases in cases_v2.
type vectors struct {
	RecipientSK     string `json:"recipient_sk"`
	KID             string `json:"kid"`
	StoreExternalID string `json:"store_external_id"`
	DirectiveID     string `json:"directive_id"`
	Type            string `json:"type"`
	PluginID        string `json:"plugin_id"`
	SettingKey      string `json:"setting_key"`
	Plaintext       string `json:"plaintext"`
	SecretSealed    string `json:"secret_sealed"`
	CasesV2         struct {
		Cases []v2Case `json:"cases"`
	} `json:"cases_v2"`
}

type declJSON struct {
	Type   string   `json:"type"`
	Scope  string   `json:"scope"`
	MaxLen int      `json:"max_len"`
	Opts   []string `json:"options"`
}

type v2Case struct {
	Name         string `json:"name"`
	DirectiveID  string `json:"directive_id"`
	PluginID     string `json:"plugin_id"`
	SettingKey   string `json:"setting_key"`
	Plaintext    string `json:"plaintext"`
	SecretSealed string `json:"secret_sealed"`
	Till         struct {
		Installed bool                `json:"installed"`
		Settings  map[string]declJSON `json:"settings"`
		LocalVer  map[string]int64    `json:"local_ver"`
	} `json:"till"`
	Expect struct {
		Outcome   string `json:"outcome"`
		ResultKey string `json:"result_key"`
		Retryable bool   `json:"retryable"`
		Written   *struct {
			Value  string            `json:"value"`
			Ver    int64             `json:"ver"`
			Origin string            `json:"origin"`
			Aux    map[string]string `json:"aux"`
		} `json:"written"`
	} `json:"expect"`
}

func loadVectors(t *testing.T) vectors {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "plugin-secret-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
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

type savedBatch struct {
	writes []Write
	ver    int64
}

type fakeSettings struct {
	local   map[string]int64 // key -> local ver
	saveErr error
	verErr  error
	saved   []savedBatch
}

func (f *fakeSettings) LocalVer(_ context.Context, _, key string) (int64, error) {
	return f.local[key], f.verErr
}

func (f *fakeSettings) Save(_ context.Context, _ string, w []Write, ver int64) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saved = append(f.saved, savedBatch{writes: w, ver: ver})
	return nil
}

type fakeManifest struct {
	d   Declared
	err error
}

func (m fakeManifest) Declared(context.Context, string) (Declared, error) { return m.d, m.err }

func declaredFrom(installed bool, in map[string]declJSON) Declared {
	d := Declared{Installed: installed, Settings: map[string]Decl{}}
	for k, s := range in {
		d.Settings[k] = Decl{Type: s.Type, Scope: s.Scope, MaxLen: s.MaxLen, Options: s.Opts}
	}
	return d
}

func newApplier(t *testing.T, v vectors, man Manifest, set Settings) *Applier {
	t.Helper()
	return &Applier{
		Keys: keysFor(t, v.RecipientSK), Settings: set, Manifest: man,
		StoreID: func() string { return v.StoreExternalID },
	}
}

func verOf(t *testing.T, plaintext string) int64 {
	t.Helper()
	var p struct {
		Ver int64 `json:"ver"`
	}
	if err := json.Unmarshal([]byte(plaintext), &p); err != nil {
		t.Fatal(err)
	}
	return p.Ver
}

func TestVectorFileHasTheSevenV2Cases(t *testing.T) {
	v := loadVectors(t)
	names := []string{}
	for _, c := range v.CasesV2.Cases {
		names = append(names, c.Name)
	}
	want := "plain_ok,secret_ok,plain_conflict,plain_stale,undeclared,kind_mismatch,restore_deferred"
	if strings.Join(names, ",") != want {
		t.Fatalf("cases_v2 = %v, want %s", names, want)
	}
}

// Every v2 vector reaches exactly its expected outcome.
func TestApply_SharedVectorsV2(t *testing.T) {
	v := loadVectors(t)
	for _, c := range v.CasesV2.Cases {
		t.Run(c.Name, func(t *testing.T) {
			set := &fakeSettings{local: c.Till.LocalVer}
			a := newApplier(t, v, fakeManifest{d: declaredFrom(c.Till.Installed, c.Till.Settings)}, set)
			msg, err := a.Apply(context.Background(), Directive{
				DirectiveID: c.DirectiveID, Type: v.Type, PluginID: c.PluginID, SettingKey: c.SettingKey,
				Ver: verOf(t, c.Plaintext), SecretSealed: c.SecretSealed,
			})
			if c.Expect.Outcome == "applied" {
				if err != nil || msg != "applied" {
					t.Fatalf("got %q %v, want applied", msg, err)
				}
				if len(set.saved) != 1 {
					t.Fatalf("want one transaction, got %d", len(set.saved))
				}
				b, w := set.saved[0], c.Expect.Written
				if b.ver != w.Ver {
					t.Fatalf("ver = %d, want %d", b.ver, w.Ver)
				}
				first := b.writes[0]
				if first.Key != c.SettingKey {
					t.Fatalf("first write = %s", first.Key)
				}
				if w.Value != "<sealed on the till>" {
					var got string
					_ = json.Unmarshal([]byte(first.ValueJSON), &got)
					if got != w.Value {
						t.Fatalf("value = %q, want %q", got, w.Value)
					}
				} else if !first.DeclaredSecret {
					t.Fatal("a secret must be written DeclaredSecret (sealed at rest)")
				}
				for k, av := range w.Aux {
					found := false
					for _, x := range b.writes[1:] {
						var got string
						_ = json.Unmarshal([]byte(x.ValueJSON), &got)
						found = found || (x.Key == k && got == av)
					}
					if !found {
						t.Fatalf("aux %s=%s not written", k, av)
					}
				}
				return
			}
			if err == nil || err.Error() != c.Expect.ResultKey {
				t.Fatalf("got %v, want %q", err, c.Expect.ResultKey)
			}
			if errors.Is(err, ErrRetryable) != c.Expect.Retryable {
				t.Fatalf("retryable = %v, want %v", errors.Is(err, ErrRetryable), c.Expect.Retryable)
			}
			if len(set.saved) != 0 {
				t.Fatal("a refusal must not write anything")
			}
		})
	}
}

// v1 stays valid: the top-level vector applies as kind secret with no
// base_ver, SumUp through the same manifest-driven path.
func TestSumUpPathStillWorks_v1Plaintext(t *testing.T) {
	v := loadVectors(t)
	set := &fakeSettings{local: map[string]int64{}}
	man := fakeManifest{d: Declared{Installed: true, Settings: map[string]Decl{
		"sumup_api_key":       {Type: "secret", Scope: "global"},
		"sumup_merchant_code": {Type: "text", Scope: "global"},
	}}}
	a := newApplier(t, v, man, set)
	msg, err := a.Apply(context.Background(), Directive{DirectiveID: v.DirectiveID, Type: v.Type,
		PluginID: v.PluginID, SettingKey: v.SettingKey, Ver: 3, SecretSealed: v.SecretSealed})
	if err != nil || msg != "applied" {
		t.Fatalf("v1 vector: %q %v", msg, err)
	}
	b := set.saved[0]
	if b.ver != 3 || len(b.writes) != 2 || b.writes[0].Key != "sumup_api_key" || !b.writes[0].DeclaredSecret ||
		b.writes[0].ValueJSON != `"sumup-fake-test-key-0000000000000000"` ||
		b.writes[1].Key != "sumup_merchant_code" || b.writes[1].ValueJSON != `"MTEST123"` {
		t.Fatalf("v1 writes = %+v", b)
	}
}

// A directive for ANY plugin's declared setting works: nothing is compiled in.
func TestApply_PlainDeclaredGlobalSettingAnyPlugin(t *testing.T) {
	v := loadVectors(t)
	c := v.CasesV2.Cases[0] // plain_ok on com.example.fixture
	if c.PluginID == "sumup" {
		t.Fatal("fixture must not be the SumUp pair")
	}
	set := &fakeSettings{local: c.Till.LocalVer}
	a := newApplier(t, v, fakeManifest{d: declaredFrom(true, c.Till.Settings)}, set)
	if _, err := a.Apply(context.Background(), Directive{DirectiveID: c.DirectiveID, Type: v.Type,
		PluginID: c.PluginID, SettingKey: c.SettingKey, Ver: 3, SecretSealed: c.SecretSealed}); err != nil {
		t.Fatal(err)
	}
}

func caseByName(t *testing.T, v vectors, name string) v2Case {
	t.Helper()
	for _, c := range v.CasesV2.Cases {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no case %s", name)
	return v2Case{}
}

func TestApply_RefusalsCarryReasonCodeOnly(t *testing.T) {
	v := loadVectors(t)
	base := caseByName(t, v, "plain_ok")
	dir := func() Directive {
		return Directive{DirectiveID: base.DirectiveID, Type: v.Type, PluginID: base.PluginID,
			SettingKey: base.SettingKey, Ver: 3, SecretSealed: base.SecretSealed}
	}
	cases := []struct {
		name   string
		mutate func(a *Applier, d *Directive)
		want   string
	}{
		{"wrong type", func(a *Applier, d *Directive) { d.Type = "set_user_pin" }, "not_allowed"},
		{"host reserved ver", func(a *Applier, d *Directive) { d.SettingKey = "ver" }, "not_allowed"},
		{"host reserved origin", func(a *Applier, d *Directive) { d.SettingKey = "origin" }, "not_allowed"},
		{"host reserved key_version", func(a *Applier, d *Directive) { d.SettingKey = "sumup_key_version" }, "not_allowed"},
		{"host reserved underscore", func(a *Applier, d *Directive) { d.SettingKey = "_internal" }, "not_allowed"},
		{"no key", func(a *Applier, d *Directive) { a.Keys = directivekey.NewAt(filepath.Join(t.TempDir(), "none")) }, "no_till_key"},
		{"kid mismatch", func(a *Applier, d *Directive) {
			d.SecretSealed = strings.Replace(d.SecretSealed, "v1."+v.KID, "v1.0000000000000000", 1)
		}, "kid_mismatch"},
		{"aad directive changed", func(a *Applier, d *Directive) { d.DirectiveID = "other" }, "open_failed"},
		{"aad plugin changed", func(a *Applier, d *Directive) { d.PluginID = "com.example.other" }, "open_failed"},
		{"malformed", func(a *Applier, d *Directive) { d.SecretSealed = "garbage" }, "open_failed"},
		{"ver mismatch with plaintext", func(a *Applier, d *Directive) { d.Ver = 4 }, "open_failed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			set := &fakeSettings{local: base.Till.LocalVer}
			a := newApplier(t, v, fakeManifest{d: declaredFrom(true, base.Till.Settings)}, set)
			d := dir()
			c.mutate(a, &d)
			_, err := a.Apply(context.Background(), d)
			if err == nil || err.Error() != c.want {
				t.Fatalf("got %v, want %q", err, c.want)
			}
			if errors.Is(err, ErrRetryable) {
				t.Fatalf("%s must be terminal", c.want)
			}
			if len(set.saved) != 0 {
				t.Fatal("a refusal must not write anything")
			}
		})
	}
}

func TestApply_ManifestUnverifiedIsRefusedTerminal(t *testing.T) {
	v := loadVectors(t)
	c := caseByName(t, v, "plain_ok")
	set := &fakeSettings{local: c.Till.LocalVer}
	a := newApplier(t, v, fakeManifest{err: plugins.ErrManifestUnverified}, set)
	_, err := a.Apply(context.Background(), Directive{DirectiveID: c.DirectiveID, Type: v.Type,
		PluginID: c.PluginID, SettingKey: c.SettingKey, Ver: 3, SecretSealed: c.SecretSealed})
	if err == nil || err.Error() != "not_allowed" || errors.Is(err, ErrRetryable) {
		t.Fatalf("got %v, want terminal not_allowed", err)
	}
}

func TestApply_ManifestUnreadableAndWriteFailureAreRetryable(t *testing.T) {
	v := loadVectors(t)
	c := caseByName(t, v, "plain_ok")
	d := Directive{DirectiveID: c.DirectiveID, Type: v.Type, PluginID: c.PluginID, SettingKey: c.SettingKey, Ver: 3, SecretSealed: c.SecretSealed}
	for name, mk := range map[string]func() *Applier{
		"manifest": func() *Applier {
			return newApplier(t, v, fakeManifest{err: errors.New("boom")}, &fakeSettings{local: c.Till.LocalVer})
		},
		"save": func() *Applier {
			return newApplier(t, v, fakeManifest{d: declaredFrom(true, c.Till.Settings)},
				&fakeSettings{local: c.Till.LocalVer, saveErr: errors.New("database is locked: MC-43")})
		},
		"localver": func() *Applier {
			return newApplier(t, v, fakeManifest{d: declaredFrom(true, c.Till.Settings)},
				&fakeSettings{local: c.Till.LocalVer, verErr: errors.New("boom")})
		},
	} {
		_, err := mk().Apply(context.Background(), d)
		if err == nil || err.Error() != "write_failed" || !errors.Is(err, ErrRetryable) {
			t.Errorf("%s: got %v, want retryable write_failed", name, err)
		}
		if err != nil && strings.Contains(err.Error(), "MC-43") {
			t.Errorf("%s: error leaks the value", name)
		}
	}
}

// restore:true is retryable only while the plugin is missing; the same
// directive with restore:false is terminal (vector restore_deferred covers
// the first; this pins the second through an installed-false manifest).
func TestApply_RestoreDeferredUntilInstalled(t *testing.T) {
	v := loadVectors(t)
	c := caseByName(t, v, "restore_deferred")
	set := &fakeSettings{local: map[string]int64{}}
	d := Directive{DirectiveID: c.DirectiveID, Type: v.Type, PluginID: c.PluginID, SettingKey: c.SettingKey, Ver: 2, SecretSealed: c.SecretSealed}
	a := newApplier(t, v, fakeManifest{d: Declared{}}, set)
	if _, err := a.Apply(context.Background(), d); err == nil || err.Error() != "plugin_not_installed" || !errors.Is(err, ErrRetryable) {
		t.Fatalf("not installed + restore: got %v, want retryable plugin_not_installed", err)
	}
	// once installed it applies
	a = newApplier(t, v, fakeManifest{d: Declared{Installed: true, Settings: map[string]Decl{"merchant_code": {Type: "text", Scope: "global"}}}}, set)
	if msg, err := a.Apply(context.Background(), d); err != nil || msg != "applied" {
		t.Fatalf("after install: %q %v", msg, err)
	}
}

func TestPlaintextShapes(t *testing.T) {
	bad := map[string]string{
		"bad json":       "not json",
		"unknown v":      `{"v":3,"ver":3,"value":"k"}`,
		"v2 unknown key": `{"v":2,"kind":"plain","op":"set","ver":3,"base_ver":1,"value":"k","evil":1}`,
		"v2 no base_ver": `{"v":2,"kind":"plain","op":"set","ver":3,"value":"k"}`,
		"v2 bad op":      `{"v":2,"kind":"plain","op":"drop","ver":3,"base_ver":1,"value":"k"}`,
		"clear w/ value": `{"v":2,"kind":"plain","op":"clear","ver":3,"base_ver":1,"value":"k"}`,
		"huge value":     `{"v":1,"ver":3,"value":"` + strings.Repeat("a", 2000) + `"}`,
		"ctrl char":      `{"v":1,"ver":3,"value":"a\nb"}`,
		"trailing json":  `{"v":2,"kind":"plain","op":"set","ver":3,"base_ver":1,"value":"k"}{}`,
	}
	for name, pt := range bad {
		if _, err := parsePlaintext([]byte(pt)); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if _, err := parsePlaintext([]byte(`{"v":2,"kind":"other","op":"set","ver":3,"base_ver":1,"value":"k"}`)); err == nil || err.Error() != "not_allowed" {
		t.Errorf("unknown kind: got %v, want not_allowed", err)
	}
	c, err := parsePlaintext([]byte(`{"v":1,"ver":4,"value":"","aux":{"sumup_merchant_code":""}}`))
	if err != nil || c.Value != "" || c.Ver != 4 || c.Kind != KindSecret || c.BaseVer != nil {
		t.Fatalf("v1 clear: %+v %v", c, err)
	}
}

func TestEncode_ValueShapesPerManifestType(t *testing.T) {
	lo, hi := int64(1), int64(5)
	cases := []struct {
		name  string
		decl  Decl
		value string
		want  string // stored value_json; "" = refused invalid_value
	}{
		{"text", Decl{Type: "text"}, "hello", `"hello"`},
		{"text too long", Decl{Type: "text", MaxLen: 3}, "hello", ""},
		{"untyped", Decl{}, "x", `"x"`},
		{"choice ok", Decl{Type: "choice", Options: []string{"live", "test"}}, "test", `"test"`},
		{"choice bad", Decl{Type: "choice", Options: []string{"live", "test"}}, "prod", ""},
		{"bool", Decl{Type: "bool"}, "true", `true`},
		{"bool bad", Decl{Type: "bool"}, "yes", ""},
		{"number", Decl{Type: "number"}, "42", `42`},
		{"number leading zero", Decl{Type: "number"}, "042", ""},
		{"number plus", Decl{Type: "number"}, "+4", ""},
		{"number below min", Decl{Type: "number", Min: &lo, Max: &hi}, "0", ""},
		{"number above max", Decl{Type: "number", Min: &lo, Max: &hi}, "6", ""},
		{"percent bps", Decl{Type: "percent"}, "2000", `2000`},
		{"percent over 100", Decl{Type: "percent"}, "10001", ""},
		{"endpoint ok", Decl{Type: "endpoint"}, "https://erp.example.com/api", `"https://erp.example.com/api"`},
		{"endpoint userinfo", Decl{Type: "endpoint"}, "https://u:p@erp.example.com", ""},
		{"endpoint ftp", Decl{Type: "endpoint"}, "ftp://erp.example.com", ""},
		{"required blank", Decl{Type: "text", Required: true}, "", ""},
	}
	for _, c := range cases {
		got, err := encode(c.decl, c.value, false)
		if c.want == "" {
			if err == nil || err.Error() != "invalid_value" {
				t.Errorf("%s: got %q %v, want invalid_value", c.name, got, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: got %q %v, want %q", c.name, got, err, c.want)
		}
	}
	for _, typ := range []string{"list", "map"} {
		if _, err := encode(Decl{Type: typ}, "x", false); err == nil || err.Error() != "not_allowed" {
			t.Errorf("%s must not be remote writable, got %v", typ, err)
		}
	}
}

func TestApply_AuxMustBeSamePluginDeclared(t *testing.T) {
	v := loadVectors(t)
	c := caseByName(t, v, "secret_ok")
	for name, mutate := range map[string]func(m map[string]declJSON){
		"aux undeclared": func(m map[string]declJSON) { delete(m, "sumup_merchant_code") },
		"aux is secret":  func(m map[string]declJSON) { m["sumup_merchant_code"] = declJSON{Type: "secret", Scope: "global"} },
		"aux not global": func(m map[string]declJSON) { m["sumup_merchant_code"] = declJSON{Type: "text", Scope: "register"} },
		"aux invalid text": func(m map[string]declJSON) {
			m["sumup_merchant_code"] = declJSON{Type: "text", Scope: "global", MaxLen: 2}
		},
	} {
		t.Run(name, func(t *testing.T) {
			settings := map[string]declJSON{}
			for k, s := range c.Till.Settings {
				settings[k] = s
			}
			mutate(settings)
			set := &fakeSettings{local: c.Till.LocalVer}
			a := newApplier(t, v, fakeManifest{d: declaredFrom(true, settings)}, set)
			_, err := a.Apply(context.Background(), Directive{DirectiveID: c.DirectiveID, Type: v.Type,
				PluginID: c.PluginID, SettingKey: c.SettingKey, Ver: 4, SecretSealed: c.SecretSealed})
			if err == nil || len(set.saved) != 0 {
				t.Fatalf("got %v saved %d, want a refusal and no write", err, len(set.saved))
			}
		})
	}
}

// A companion changed on the till after the cloud looked is never
// overwritten: conflict, nothing written (not even the secret).
func TestApply_AuxEditedOnTheTillIsAConflict(t *testing.T) {
	v := loadVectors(t)
	c := caseByName(t, v, "secret_ok")
	set := &fakeSettings{local: map[string]int64{"sumup_api_key": 3, "sumup_merchant_code": 9}}
	a := newApplier(t, v, fakeManifest{d: declaredFrom(true, c.Till.Settings)}, set)
	_, err := a.Apply(context.Background(), Directive{DirectiveID: c.DirectiveID, Type: v.Type,
		PluginID: c.PluginID, SettingKey: c.SettingKey, Ver: 4, SecretSealed: c.SecretSealed})
	if err == nil || err.Error() != "conflict" || len(set.saved) != 0 {
		t.Fatalf("got %v saved %d, want conflict and no write", err, len(set.saved))
	}
}

func TestSecretKeysFor_FromTheManifestOnly(t *testing.T) {
	man := fakeManifest{d: Declared{Installed: true, Settings: map[string]Decl{
		"b_token":       {Type: "secret", Scope: "global"},
		"a_key":         {Type: "secret", Scope: "global"},
		"per_till":      {Type: "secret", Scope: "register"},
		"plain":         {Type: "text", Scope: "global"},
		"x_key_version": {Type: "secret", Scope: "global"},
	}}}
	got := SecretKeysFor(context.Background(), man, "p")
	if strings.Join(got, ",") != "a_key,b_token" {
		t.Fatalf("keys = %v, want [a_key b_token]", got)
	}
	if k := SecretKeysFor(context.Background(), fakeManifest{err: errors.New("x")}, "p"); k != nil {
		t.Fatalf("unreadable manifest declares %v, want none", k)
	}
	if k := SecretKeysFor(context.Background(), fakeManifest{}, "p"); k != nil {
		t.Fatalf("not installed declares %v, want none", k)
	}
}

// The error text of every refusal is a bare code; the canary value never
// appears in one.
func TestApply_ErrorsNeverCarryTheValue(t *testing.T) {
	v := loadVectors(t)
	for _, c := range v.CasesV2.Cases {
		set := &fakeSettings{local: c.Till.LocalVer, saveErr: errors.New("db: MC-43 sumup-fake-test-key-1111111111111111")}
		a := newApplier(t, v, fakeManifest{d: declaredFrom(c.Till.Installed, c.Till.Settings)}, set)
		_, err := a.Apply(context.Background(), Directive{DirectiveID: c.DirectiveID, Type: v.Type,
			PluginID: c.PluginID, SettingKey: c.SettingKey, Ver: verOf(t, c.Plaintext), SecretSealed: c.SecretSealed})
		if err != nil && (strings.Contains(err.Error(), "MC-4") || strings.Contains(err.Error(), "sumup-fake") || len(err.Error()) > 40) {
			t.Errorf("%s: error %q is not a bare reason code", c.Name, err)
		}
	}
}

// racySettings reports an old version to the first check and a newer one
// (a till edit in between) inside the write transaction.
type racySettings struct {
	fakeSettings
	editedTo int64
	saved2   bool
}

func (f *racySettings) SaveChecked(ctx context.Context, pluginID string, w []Write, ver int64,
	check func(func(string) (int64, error)) error) error {
	if err := check(func(string) (int64, error) { return f.editedTo, nil }); err != nil {
		return err
	}
	f.saved2 = true
	return nil
}

func TestApply_TillEditBetweenCheckAndWriteIsNotOverwritten(t *testing.T) {
	v := loadVectors(t)
	c := v.CasesV2.Cases[0]
	set := &racySettings{fakeSettings: fakeSettings{local: c.Till.LocalVer}, editedTo: 1 << 40}
	a := newApplier(t, v, fakeManifest{d: declaredFrom(true, c.Till.Settings)}, set)
	_, err := a.Apply(context.Background(), Directive{DirectiveID: c.DirectiveID, Type: v.Type,
		PluginID: c.PluginID, SettingKey: c.SettingKey, Ver: 3, SecretSealed: c.SecretSealed})
	if err == nil || set.saved2 {
		t.Fatalf("a till edit inside the transaction must refuse the write: err=%v saved=%v", err, set.saved2)
	}
	if err.Error() != ReasonStaleVersion && err.Error() != ReasonConflict {
		t.Errorf("want a stale_version or conflict reason, got %v", err)
	}
}
