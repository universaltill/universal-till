package pluginsettingsreport

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/plugins"
)

// canary is a fake credential no wire byte may ever contain (ADR-0214, ut-docs#6969).
const canary = "CANARY-sk_live_0123456789abcdefXYZ4821"

var t0 = time.Date(2031, 1, 2, 3, 4, 5, 0, time.UTC) // fixed-date:ok fixture constant, not read against the clock

func yes() *bool { f := true; return &f }
func no() *bool  { f := false; return &f }

func marshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestBuild_NonSecretValueAndSecretSetOnly(t *testing.T) {
	in := []Plugin{{ID: "p.one", Version: "1.0.0",
		Settings: []plugins.ManifestSetting{
			{Key: "merchant_code", Type: "text"},
			{Key: "flavour", Type: "choice", Options: []plugins.ManifestSettingOption{{Value: "a"}, {Value: "b"}}},
			{Key: "access", Type: "secret", SecretLast4: true},
		},
		Rows: map[string]Row{
			"merchant_code": {ValueJSON: `"M123"`, UpdatedAt: t0},
			"flavour":       {ValueJSON: `"b"`, UpdatedAt: t0},
			"access":        {ValueJSON: `"` + canary + `"`, UpdatedAt: t0},
		}}}
	out := Build(in)
	wire := marshal(t, out)
	if strings.Contains(wire, canary) {
		t.Fatalf("a secret value crossed the report: %s", wire)
	}
	if len(out) != 1 || len(out[0].Settings) != 3 {
		t.Fatalf("want 1 plugin with 3 settings, got %s", wire)
	}
	s := out[0].Settings
	if s[0].Value == nil || *s[0].Value != "M123" || s[0].Ver != 1 || s[0].Origin != "till" || s[0].At != "2031-01-02T03:04:05Z" {
		t.Fatalf("text row = %+v", s[0])
	}
	if s[1].Value == nil || *s[1].Value != "b" {
		t.Fatalf("choice row = %+v", s[1])
	}
	if s[2].Value != nil || s[2].Set == nil || !*s[2].Set || s[2].Last4 != "4821" {
		t.Fatalf("secret row = %+v", s[2])
	}
}

func TestBuild_Last4OnlyWhenManifestAllowsAndLongEnough(t *testing.T) {
	mk := func(last4 bool, v string) Setting {
		out := Build([]Plugin{{ID: "p", Version: "1", Settings: []plugins.ManifestSetting{{Key: "access", Type: "secret", SecretLast4: last4}},
			Rows: map[string]Row{"access": {ValueJSON: `"` + v + `"`, UpdatedAt: t0}}}})
		return out[0].Settings[0]
	}
	if got := mk(false, "0123456789abcdef"); got.Last4 != "" {
		t.Fatalf("manifest did not allow last4, got %q", got.Last4)
	}
	if got := mk(true, "0123456789abcde"); got.Last4 != "" { // 15 chars
		t.Fatalf("15 chars must not report last4, got %q", got.Last4)
	}
	if got := mk(true, "0123456789abcdef"); got.Last4 != "cdef" { // 16 chars
		t.Fatalf("16 chars with opt-in want last4 cdef, got %q", got.Last4)
	}
}

func TestBuild_SecretNotSetAndUnreadable(t *testing.T) {
	out := Build([]Plugin{{ID: "p", Version: "1", Settings: []plugins.ManifestSetting{
		{Key: "a", Type: "secret"}, {Key: "b", Type: "secret"}, {Key: "c", Type: "secret"}},
		Rows: map[string]Row{
			"a": {ValueJSON: `""`, UpdatedAt: t0},
			"b": {ValueJSON: "", Unreadable: true, UpdatedAt: t0},
			// c has no row at all
		}}})
	for i, s := range out[0].Settings {
		if s.Set == nil || *s.Set || s.Last4 != "" || s.Value != nil {
			t.Fatalf("setting %d = %+v, want set:false", i, s)
		}
	}
}

func TestBuild_WithheldValues(t *testing.T) {
	in := []Plugin{{ID: "p", Version: "1", Settings: []plugins.ManifestSetting{
		{Key: "webhook_secret_name", Type: "text"}, // name looks secret, not declared secret
		{Key: "region", Type: "text", Report: no()},
		{Key: "base", Type: "endpoint"},
		{Key: "long", Type: "text"},
		{Key: "bad_choice", Type: "choice", Options: []plugins.ManifestSettingOption{{Value: "x"}}},
		{Key: "items", Type: "list"},
		{Key: "per_till", Type: "text", Scope: "register"},
		{Key: "fine", Type: "text", Report: yes()},
	}, Rows: map[string]Row{
		"webhook_secret_name": {ValueJSON: `"` + canary + `"`, UpdatedAt: t0},
		"region":              {ValueJSON: `"eu-west-private"`, UpdatedAt: t0},
		"base":                {ValueJSON: `"https://user:` + canary + `@host.example/x"`, UpdatedAt: t0},
		"long":                {ValueJSON: `"` + strings.Repeat("a", 257) + `"`, UpdatedAt: t0},
		"bad_choice":          {ValueJSON: `"y"`, UpdatedAt: t0},
		"items":               {ValueJSON: `[1,2]`, UpdatedAt: t0},
		"per_till":            {ValueJSON: `"x"`, UpdatedAt: t0},
		"fine":                {ValueJSON: `"ok"`, UpdatedAt: t0},
	}}}
	out := Build(in)
	wire := marshal(t, out)
	for _, bad := range []string{canary, "eu-west-private", "aaaaaaaaaa", `"y"`, `[1,2]`, "per_till", "items"} {
		if strings.Contains(wire, bad) {
			t.Fatalf("%q must not be on the wire: %s", bad, wire)
		}
	}
	by := map[string]Setting{}
	for _, s := range out[0].Settings {
		by[s.Key] = s
	}
	for _, k := range []string{"webhook_secret_name", "region", "base"} {
		if s, ok := by[k]; !ok || s.Value != nil || s.Set == nil || !*s.Set || s.Last4 != "" {
			t.Fatalf("%s = %+v, want set-only", k, s)
		}
	}
	if _, ok := by["long"]; ok {
		t.Fatalf("an over-long value is dropped, got %+v", by["long"])
	}
	if s := by["fine"]; s.Value == nil || *s.Value != "ok" {
		t.Fatalf("fine = %+v", s)
	}
}

func TestBuild_DeterministicAndCapped(t *testing.T) {
	var in []Plugin
	for i := 0; i < MaxPlugins+5; i++ {
		id := "p." + string(rune('a'+i%26)) + string(rune('a'+i/26))
		in = append(in, Plugin{ID: id, Version: "1", Settings: []plugins.ManifestSetting{{Key: "k", Type: "text"}},
			Rows: map[string]Row{"k": {ValueJSON: `"v"`, UpdatedAt: t0}}})
	}
	a, b := marshal(t, Build(in)), marshal(t, Build(in))
	if a != b {
		t.Fatal("report must be byte-identical for the same input")
	}
	if n := len(Build(in)); n != MaxPlugins {
		t.Fatalf("plugins = %d, want cap %d", n, MaxPlugins)
	}
	if got := Build(nil); got == nil || len(got) != 0 {
		t.Fatalf("no plugins must be an empty non-nil list, got %#v", got)
	}
}

// The contract vector is pinned byte-for-byte in ut-cloud too (ut-docs
// reference/contracts/till-plugin-settings-v1.json): both repos must read
// the same wire form.
func TestBuild_ContractVector(t *testing.T) {
	raw, err := os.ReadFile("testdata/till-plugin-settings-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	// Byte-identical in ut-docs reference/contracts/till-plugin-settings-v1.json and
	// ut-cloud internal/claims/testdata (the same pin there). Change all three together.
	if sum := sha256.Sum256(raw); hex.EncodeToString(sum[:]) != "b633e68010c38f0e58d528df1abd6bc942ba9f840a98025fc8f3052dc0d57444" {
		t.Fatalf("contract vector changed (sha256 %x): update it in ut-docs, universal-till and ut-cloud together", sum)
	}
	var vec struct {
		Input []struct {
			ID       string                    `json:"id"`
			Version  string                    `json:"version"`
			Settings []plugins.ManifestSetting `json:"settings"`
			Rows     map[string]struct {
				ValueJSON  string `json:"value_json"`
				Unreadable bool   `json:"unreadable"`
				UpdatedAt  string `json:"updated_at"`
			} `json:"rows"`
		} `json:"input"`
		Wire json.RawMessage `json:"wire"`
	}
	if err := json.Unmarshal(raw, &vec); err != nil {
		t.Fatal(err)
	}
	var in []Plugin
	for _, p := range vec.Input {
		pl := Plugin{ID: p.ID, Version: p.Version, Settings: p.Settings, Rows: map[string]Row{}}
		for k, r := range p.Rows {
			at, _ := time.Parse(time.RFC3339, r.UpdatedAt)
			pl.Rows[k] = Row{ValueJSON: r.ValueJSON, Unreadable: r.Unreadable, UpdatedAt: at}
		}
		in = append(in, pl)
	}
	var want any
	if err := json.Unmarshal(vec.Wire, &want); err != nil {
		t.Fatal(err)
	}
	var got any
	if err := json.Unmarshal([]byte(marshal(t, Build(in))), &got); err != nil {
		t.Fatal(err)
	}
	if marshal(t, got) != marshal(t, want) {
		t.Fatalf("wire mismatch\n got: %s\nwant: %s", marshal(t, got), marshal(t, want))
	}
	if strings.Contains(string(raw), "CANARY") && strings.Contains(marshal(t, got), "CANARY") {
		t.Fatal("canary on the wire")
	}
}

// A URL setting that is not typed "endpoint" (a webhook URL, an image
// endpoint) still never reports a value that carries credentials (review of
// ut-docs#6969).
func TestBuild_UntypedURLWithCredentialsIsWithheld(t *testing.T) {
	rows := map[string]Row{
		"hook":  {ValueJSON: `"https://user:` + canary + `@host.example/h"`, UpdatedAt: t0},
		"hook2": {ValueJSON: `"https://host.example/h?token=` + canary + `"`, UpdatedAt: t0},
		"plain": {ValueJSON: `"https://host.example/h"`, UpdatedAt: t0},
	}
	out := Build([]Plugin{{ID: "p", Version: "1", Rows: rows, Settings: []plugins.ManifestSetting{
		{Key: "hook"}, {Key: "hook2", Type: "text"}, {Key: "plain"}}}})
	if strings.Contains(marshal(t, out), canary) {
		t.Fatalf("a URL credential crossed the report: %s", marshal(t, out))
	}
	by := map[string]Setting{}
	for _, s := range out[0].Settings {
		by[s.Key] = s
	}
	if by["hook"].Value != nil || by["hook2"].Value != nil || by["hook"].Set == nil || !*by["hook"].Set {
		t.Fatalf("credential URLs must be set-only: %+v %+v", by["hook"], by["hook2"])
	}
	if by["plain"].Value == nil || *by["plain"].Value != "https://host.example/h" {
		t.Fatalf("a clean URL reports its value: %+v", by["plain"])
	}
}

func TestPrintable_KeepsZWNJRejectsOtherFormatChars(t *testing.T) {
	if !printable("می‌خواهم") {
		t.Fatal("Persian ZWNJ must be reportable")
	}
	for _, bad := range []string{"a​b", "a‮b", "a\x00b", "a�b", "a\xffb"} {
		if printable(bad) {
			t.Fatalf("%q must not be reportable", bad)
		}
	}
}
