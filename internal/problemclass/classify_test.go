package problemclass

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/logging"
)

func TestClassify_KnownErrors(t *testing.T) {
	cases := []struct {
		msg   string
		area  Area
		known Known
		id    string
	}{
		{"cloudsync: push: cloud answered 403 not_main_till: not the main till", AreaCloudSync, KnownNotMainTill, "not_main_till"},
		{"cloudsync: cloud answered 426 till_too_old: upgrade", AreaCloudSync, KnownTillTooOld, "till_too_old"},
		{"[Download] checksum mismatch for x", AreaPlugins, KnownChecksumMismatch, "checksum_mismatch"},
		{"wasm sync: module closed with exit_code(2)", AreaPlugins, KnownPluginExit, "plugin_exit"},
		{"wasm handler: exit code 2", AreaPlugins, KnownPluginExit, "plugin_exit"},
		{"marketplace: no catalog snapshot", AreaPlugins, KnownNoCatalogSnapshot, "no_catalog_snapshot"},
	}
	for _, c := range cases {
		got := Classify(logging.Problem{Level: "WARN", Msg: c.msg})
		if got.Area != c.area || got.Known != c.known {
			t.Errorf("%q: area=%s known=%s, want %s/%s", c.msg, got.Area, got.Known, c.area, c.known)
		}
		if got.ReasonKey != "backoffice.problems."+c.id+".reason" || got.NextKey != "backoffice.problems."+c.id+".next" {
			t.Errorf("%q: keys %q %q", c.msg, got.ReasonKey, got.NextKey)
		}
	}
}

func TestClassify_AreaByPrefix(t *testing.T) {
	cases := []struct {
		p    logging.Problem
		area Area
	}{
		{logging.Problem{Msg: "wasm sync: x"}, AreaPlugins},
		{logging.Problem{Msg: "[PluginUpdateScheduler] x"}, AreaPlugins},
		{logging.Problem{Msg: "[Backup] x"}, AreaDatabase},
		{logging.Problem{Msg: "free space low", Key: "disk.low"}, AreaDatabase},
		{logging.Problem{Msg: "paybroker: x"}, AreaPayments},
		{logging.Problem{Msg: "lantls: x"}, AreaNetwork},
		{logging.Problem{Msg: "sync pull: x"}, AreaNetwork}, // LAN main-till link, not the cloud (review M1)
		{logging.Problem{Msg: "sync push: x too large for the main till"}, AreaNetwork},
		{logging.Problem{Msg: "main till unreachable", Key: "sync.main_till_contact"}, AreaNetwork},
		{logging.Problem{Msg: "builtin locale bundle: en: catalog unreachable: refused"}, AreaNetwork},
		{logging.Problem{Msg: "[selfupdate] x"}, AreaPlugins},
		{logging.Problem{Msg: "Android update refused"}, AreaPlugins},
		{logging.Problem{Msg: "cloudsync: push: x"}, AreaCloudSync}, // the commonest real prefix (tester, ut-docs#6978)
		{logging.Problem{Msg: "something odd"}, AreaOther},
	}
	for _, c := range cases {
		got := Classify(c.p)
		if got.Area != c.area {
			t.Errorf("%+v: area %s, want %s", c.p, got.Area, c.area)
		}
		if got.Known != "" {
			t.Errorf("%+v: unexpected known %s", c.p, got.Known)
		}
		if want := "backoffice.problems.generic." + string(c.area) + ".reason"; got.ReasonKey != want {
			t.Errorf("%+v: reason key %q, want %q", c.p, got.ReasonKey, want)
		}
	}
}

func TestSeverityOf(t *testing.T) {
	for in, want := range map[string]Severity{
		"WARN": SeverityAttention, "warn": SeverityAttention,
		"ERROR": SeverityFailed, "FATAL": SeverityFailed, "error": SeverityFailed,
	} {
		if got := SeverityOf(in); got != want {
			t.Errorf("SeverityOf(%q)=%v want %v", in, got, want)
		}
	}
	if SeverityOK.String() != "ok" || SeverityAttention.String() != "attention" || SeverityFailed.String() != "failed" {
		t.Error("Severity.String")
	}
}

func TestClassify_KeysExistInEnJSON(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "web", "locales", "en.json"))
	if err != nil {
		t.Fatal(err)
	}
	en := map[string]string{}
	if err := json.Unmarshal(raw, &en); err != nil {
		t.Fatal(err)
	}
	// The template builds these titles dynamically, so guard-i18n cannot see
	// them (review L4).
	for _, a := range Areas {
		if k := "backoffice.problems.area." + string(a); en[k] == "" {
			t.Errorf("area title %q missing from en.json", k)
		}
	}
	for _, s := range []Severity{SeverityOK, SeverityAttention, SeverityFailed} {
		if k := "backoffice.problems.severity." + s.String(); en[k] == "" {
			t.Errorf("severity key %q missing from en.json", k)
		}
	}
	for _, k := range AllKeys() {
		if en[k] == "" {
			t.Errorf("key %q missing from en.json", k)
		}
	}
	if len(AllKeys()) < 2*(len(knownRules)+len(Areas)) {
		t.Errorf("AllKeys incomplete: %d", len(AllKeys()))
	}
}
