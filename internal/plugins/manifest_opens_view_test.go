package plugins

import (
	"context"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ADR-0149 §6 (ut-docs#3699): a `button` entry may declare opens_view, the
// view of one of the plugin's own page entries. Pressing the button opens
// that view in the sell-screen overlay. The page entry must be under
// /plugin/ and fill no content slot (a slot entry's route is gated by its
// slot's host screen, which the overlay is not).

const opensViewPage = `{"type":"page","key":"log","label":"Log","route":"/plugin/t/log","view":"t.log"}`

func opensViewEntries(button string, pages ...string) string {
	all := append([]string{button}, pages...)
	return `,"entries":[` + strings.Join(all, ",") + `]`
}

func TestParseManifest_ButtonOpensView_3699(t *testing.T) {
	good := `{"type":"button","key":"open","label":"Open","opens_view":"t.log"}`
	m, err := ParseManifest(strings.NewReader(manifestWith(opensViewEntries(good, opensViewPage))))
	if err != nil {
		t.Fatalf("valid opens_view refused: %v", err)
	}
	if m.Entries[0].OpensView != "t.log" {
		t.Fatalf("opens_view not parsed: %+v", m.Entries[0])
	}

	cases := []struct {
		name, entries string
	}{
		{"no page entry with that view", opensViewEntries(`{"type":"button","key":"open","label":"Open","opens_view":"t.other"}`, opensViewPage)},
		{"no page entries at all", opensViewEntries(`{"type":"button","key":"open","label":"Open","opens_view":"t.log"}`)},
		{"bad view name", opensViewEntries(`{"type":"button","key":"open","label":"Open","opens_view":"T Log"}`, opensViewPage)},
		{"on a page entry", opensViewEntries(`{"type":"page","key":"p2","label":"P","route":"/plugin/t/p2","view":"t.p2","opens_view":"t.log"}`, opensViewPage)},
		{"page outside /plugin/", opensViewEntries(`{"type":"button","key":"open","label":"Open","opens_view":"t.log"}`,
			`{"type":"page","key":"log","label":"Log","route":"/t-log","view":"t.log"}`)},
		{"page fills a slot", opensViewEntries(`{"type":"button","key":"open","label":"Open","opens_view":"t.log"}`,
			`{"type":"page","key":"log","label":"Log","route":"/plugin/t/log","view":"t.log","slot":"reports.panels"}`)},
		{"view on a non-page entry", opensViewEntries(`{"type":"button","key":"open","label":"Open","opens_view":"t.log"}`,
			`{"type":"button","key":"other","label":"O","route":"/plugin/t/log","view":"t.log"}`)},
	}
	for _, c := range cases {
		_, err := ParseManifest(strings.NewReader(manifestWith(c.entries)))
		if err == nil {
			t.Errorf("%s: accepted", c.name)
			continue
		}
		if !strings.Contains(err.Error(), "opens_view") {
			t.Errorf("%s: error %q does not name opens_view", c.name, err)
		}
	}
}

// PersistManifest runs the same check (a manifest built in code never went
// through ParseManifest), and a good one is read back on the button row.
// A config key "opens_view" smuggled past the typed field never persists.
func TestPersistManifest_ButtonOpensView_3699(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()

	bad := pageIconManifest("com.opens.bad", "obad", "")
	bad.Entries[0].View = "obad.log"
	bad.Entries = append(bad.Entries, ManifestEntry{Type: "button", Key: "open", Label: "Open", OpensView: "obad.nope"})
	if err := PersistManifest(ctx, d.DB, bad, InstallOptions{}); err == nil || !strings.Contains(err.Error(), "opens_view") {
		t.Fatalf("PersistManifest with an unknown opens_view: err = %v, want a refusal naming opens_view", err)
	}

	good := pageIconManifest("com.opens.good", "ogood", "")
	good.Entries[0].View = "ogood.log"
	good.Entries = append(good.Entries,
		ManifestEntry{Type: "button", Key: "open", Label: "Open", OpensView: "ogood.log"},
		ManifestEntry{Type: "button", Key: "smuggled", Label: "S", Config: map[string]interface{}{"opens_view": "ogood.log"}},
	)
	if err := PersistManifest(ctx, d.DB, good, InstallOptions{}); err != nil {
		t.Fatalf("PersistManifest: %v", err)
	}
	rows, err := data.NewPluginRepo(d.DB).ListButtonEntries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, b := range rows {
		if b.PluginID == "com.opens.good" {
			got[b.EntryKey] = b.OpensView
		}
	}
	if got["open"] != "ogood.log" {
		t.Fatalf("button open: OpensView = %q, want ogood.log (rows %+v)", got["open"], got)
	}
	if v, ok := got["smuggled"]; !ok || v != "" {
		t.Fatalf("button smuggled: OpensView = %q (present %v), want \"\" -- only the typed field counts", v, ok)
	}
}
