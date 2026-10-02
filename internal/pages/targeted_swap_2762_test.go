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
	for _, a := range []string{"UT.refreshRegion(ctx.el)", "UT.refreshRegion(t)"} {
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
