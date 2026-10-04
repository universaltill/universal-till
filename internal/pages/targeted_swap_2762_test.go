package pages

import (
	"os"
	"strings"
	"testing"
)

// ut-docs#2762: /users and /shifts (and the elevation dialog's retry, which
// lands on /users' forms) refresh only the changed region via
// UT.refreshRegion instead of reloading the whole page. Pin both halves:
// no reload call left, and the region each page names actually exists.
func TestUsersShiftsSwapRegionNotReload(t *testing.T) {
	cases := []struct {
		file    string
		anchors []string
	}{
		// ut-docs#3325: the calls live in web/public/inline-actions.js's
		// refresh-region / elevation-done steps (pinned below).
		{"web/ui/pages/users.html", []string{`data-ut-refresh="#users-list"`, `id="users-list"`, "ut-ok refresh-region"}},
		{"web/ui/pages/shifts.html", []string{`<div id="shifts-page" data-ut-refresh="#shifts-page">`, "ok refresh-region"}},
		{"web/ui/partials/elevation_prompt.html", []string{`data-after-request="elevation-done"`}},
	}
	for _, c := range cases {
		b, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		if strings.Contains(s, "location.reload()") {
			t.Errorf("%s still calls location.reload(); swap the region with UT.refreshRegion instead", c.file)
		}
		for _, a := range c.anchors {
			if !strings.Contains(s, a) {
				t.Errorf("%s: missing %q", c.file, a)
			}
		}
	}
	js, err := os.ReadFile("web/public/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(js), "UT.refreshRegion = function") {
		t.Error("web/public/app.js: UT.refreshRegion helper missing")
	}
	// The steps the templates above name (refresh-region, elevation-done)
	// are what actually call it (ut-docs#3325).
	acts, err := os.ReadFile("web/public/inline-actions.js")
	if err != nil {
		t.Fatal(err)
	}
	// refresh-region falls back to ctx.el when no region id is named (ut-docs#2904).
	for _, a := range []string{"UT.refreshRegion(r || ctx.el)", "UT.refreshRegion(t)"} {
		if !strings.Contains(string(acts), a) {
			t.Errorf("web/public/inline-actions.js: missing %q", a)
		}
	}
}

// ut-docs#2762 slice 2: Bluetooth Pair/Forget refresh only the paired
// card, and the barcode backfill's Close refreshes only the catalog grid.
func TestBluetoothBackfillSwapRegionNotReload(t *testing.T) {
	cases := []struct {
		file    string
		anchors []string
	}{
		{"web/ui/pages/bluetooth_devices.html", []string{`id="bt-paired" data-ut-refresh="#bt-paired"`, `id="bt-layout"`, "UT.refreshRegion(pairedMsg())"}},
		{"web/ui/partials/catalog_barcode_backfill.html", []string{"close:barcode-backfill-modal refresh-region"}},
		{"web/ui/pages/catalog.html", []string{`data-ut-refresh="#catalog-table"`, "'ut:region-refreshed'"}},
		{"web/ui/partials/catalog_table.html", []string{`id="catalog-table"`}},
	}
	for _, c := range cases {
		b, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		if strings.Contains(s, "location.reload()") {
			t.Errorf("%s still calls location.reload(); swap the region with UT.refreshRegion instead", c.file)
		}
		for _, a := range c.anchors {
			if !strings.Contains(s, a) {
				t.Errorf("%s: missing %q", c.file, a)
			}
		}
	}
	js, err := os.ReadFile("web/public/app.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(js), "new CustomEvent('ut:region-refreshed'") {
		t.Error("web/public/app.js: UT.refreshRegion must dispatch ut:region-refreshed")
	}
}

// ut-docs#2902: Settings forms whose change is local to their own card
// refresh only a region around that form; the forms that restyle or
// re-label the whole shell keep their reload. Regions sit INSIDE the
// .card, never on it: the section switcher holds references to the card
// elements, and a replaced card would fall out of its nav.
func TestSettingsSwapRegionNotReload(t *testing.T) {
	b, err := os.ReadFile("web/ui/pages/settings.html")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	swapped := []struct{ post, region string }{
		{"/api/settings/basket-panel-width", "settings-basket-width-region"},
		{"/api/settings/report-retention", "settings-retention-region"},
		{"/api/settings/printer", "settings-printer-region"},
		{"/api/settings/till-name", "settings-till-name-region"},
		{"/api/settings/till-register", "settings-till-register-region"},
		{"/api/settings/invoice", "settings-invoice-region"},
		{"/api/settings/telemetry", "settings-telemetry-region"},
		{"/api/settings/store-name", "settings-store-name-region"},
	}
	for _, c := range swapped {
		anchor := `id="` + c.region + `" data-ut-refresh="#` + c.region + `" data-ut-saved-msg="#`
		at := strings.Index(s, anchor)
		if at < 0 {
			t.Errorf("settings.html: missing region %q", anchor)
			continue
		}
		form := formTag(t, s, c.post)
		if strings.Contains(form, "reload:") {
			t.Errorf("settings.html: %s still reloads the page: %s", c.post, form)
		}
		if !strings.Contains(form, "ok not-html refresh-region") {
			t.Errorf("settings.html: %s must refresh its region behind the not-html guard: %s", c.post, form)
		}
		if fi := strings.Index(s, form); fi < at {
			t.Errorf("settings.html: %s's form is not inside #%s", c.post, c.region)
		}
	}
	// The whole shell changes for these: theme/scale/effects/OSK restyle it,
	// currency/language/staff languages re-format or re-label it, the
	// idle-lock timeout is read once from <body data-idle-lock>, and opting
	// in to auto-register registers now, which drops the status bar's
	// "Register till" chip.
	kept := map[string]string{
		"/api/settings/auto-register":   "reload:settings-auto-register",
		"/api/settings/theme":           "reload:settings-theme",
		"/api/settings/ui-scale":        "reload:settings-ui-scale",
		"/api/settings/effects-level":   "reload:settings-effects-level",
		"/api/settings/osk":             "reload:settings-osk",
		"/api/settings/idle-lock":       "reload:settings-idle-lock",
		"/api/settings/staff-languages": "reload:settings-save",
	}
	for post, want := range kept {
		if form := formTag(t, s, post); !strings.Contains(form, want) {
			t.Errorf("settings.html: %s must keep %q: %s", post, want, form)
		}
	}
	if n := strings.Count(s, `hx-post="/api/settings/save" hx-swap="none" data-after-request="ok not-html reload:settings-save;`); n != 2 {
		t.Errorf("settings.html: currency and language must both keep reload:settings-save, found %d", n)
	}
	if strings.Contains(s, `class="card" data-ut-refresh`) || strings.Contains(s, `class="card settings-wide" data-ut-refresh`) {
		t.Error("settings.html: a .card must never be a refresh region (the section switcher holds card references)")
	}
	// A swap re-renders identical content, so the switcher says "Saved." in
	// the region's data-ut-saved-msg span and drops the stale search index.
	for _, a := range []string{"grid.addEventListener('ut:region-refreshed'", "index = null;", `T "settings.display.change_saved"`, `id="basket-panel-msg"`} {
		if !strings.Contains(s, a) {
			t.Errorf("settings.html: missing %q", a)
		}
	}
	// The printer region holds the discovery button; its listener must
	// survive a swap, so it is delegated from the card, not bound once.
	if strings.Contains(s, "getElementById('printer-discover-btn')") || !strings.Contains(s, "closest('#printer-discover-btn')") {
		t.Error("settings.html: printer discovery must use a delegated listener that survives the region swap")
	}
}

// formTag returns the opening <form …> tag that posts to path.
func formTag(t *testing.T, s, path string) string {
	t.Helper()
	i := strings.Index(s, `hx-post="`+path+`"`)
	if i < 0 {
		t.Fatalf("settings.html: no form posts to %s", path)
	}
	start := strings.LastIndex(s[:i], "<form")
	end := strings.Index(s[i:], ">")
	if start < 0 || end < 0 {
		t.Fatalf("settings.html: malformed form for %s", path)
	}
	return s[start : i+end+1]
}

// ut-docs#2904: pairing approve/deny, till revoke and Backup now refresh
// only their own card instead of the handler forcing HX-Refresh. The
// handler half is pinned by the per-handler tests (pending_pairings_test,
// sync_api_test, backup_api_test); this pins the template half.
func TestPairingRevokeBackupSwapRegionNotReload(t *testing.T) {
	cases := []struct {
		file    string
		anchors []string
	}{
		{"web/ui/pages/tills.html", []string{
			`id="tills-pairing-card" data-ut-refresh="#tills-pairing-card"`,
			`id="tills-roster" data-ut-refresh="#tills-roster"`,
		}},
		{"web/ui/partials/pending_pairings.html", []string{
			`data-after-request="ok refresh-region:tills-pairing-card; fail unhide:pin-error-{{ .ID }}"`,
		}},
		{"web/ui/partials/tills_roster.html", []string{
			`hx-post="/api/sync/tills/{{ .ID }}/revoke"`,
			`data-after-request="ok refresh-region:tills-roster"`,
		}},
		{"web/ui/pages/settings.html", []string{
			`<div id="settings-backup-region" data-ut-refresh="#settings-backup-region">`,
			`hx-post="/api/backup/now" hx-target="#backup-msg" hx-swap="innerHTML" data-after-request="ut-ok refresh-region"`,
		}},
		// The handlers' HX-Trigger: tills-changed replaces what the reload
		// used to do for these two 30s polls.
		{"web/ui/partials/nav.html", []string{
			`id="sync-chip" hx-preserve hx-get="/ui/sync-chip" hx-trigger="load, every 30s, tills-changed from:body"`,
		}},
		{"web/ui/layouts/base.html", []string{
			`id="pairing-notice-mount" hx-get="/ui/pairing-notice" hx-trigger="load, every 30s, tills-changed from:body"`,
		}},
	}
	for _, c := range cases {
		b, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		for _, a := range c.anchors {
			if !strings.Contains(s, a) {
				t.Errorf("%s: missing %q", c.file, a)
			}
		}
	}
	// The backup region sits inside the card (the section switcher holds
	// the card element) and ends before #restore-msg (the staged-restore
	// partial's inline script would not re-run after a region swap).
	b, err := os.ReadFile("web/ui/pages/settings.html")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if strings.Contains(s, `id="settings-backup" data-ut-refresh`) {
		t.Error("settings.html: #settings-backup itself must not be a refresh region")
	}
	card := strings.Index(s, `id="settings-backup"`)
	region := strings.Index(s, `id="settings-backup-region"`)
	restore := strings.Index(s, `id="restore-msg"`)
	if card < 0 || region < card || restore < region {
		t.Fatalf("settings.html: want card < region < #restore-msg, got %d %d %d", card, region, restore)
	}
	// The region's own closing tag must come before #restore-msg: count
	// <div opens vs </div> closes from the region start to #restore-msg.
	seg := s[region:restore]
	if opens, closes := strings.Count(seg, "<div"), strings.Count(seg, "</div>"); closes < opens {
		t.Errorf("settings.html: #restore-msg is inside #settings-backup-region (%d <div vs %d </div>)", opens, closes)
	}
	acts, err := os.ReadFile("web/public/inline-actions.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(acts), "UT.refreshRegion(r || ctx.el)") {
		t.Error("web/public/inline-actions.js: refresh-region:<id> must resolve the named region")
	}
}
