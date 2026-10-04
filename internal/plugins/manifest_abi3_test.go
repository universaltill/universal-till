package plugins

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ADR-0121 §2 (ut-docs#3155): the ABI-3 manifest fields parse, are
// validated at both install paths, never change the canonical bytes of a
// manifest that omits them, and their limits are clamped without touching
// the signed manifest.

func TestParseManifest_ABI3FieldsRoundTrip(t *testing.T) {
	src := manifestWith(`,"wasm_abi":2,
		"limits":{"memory_mb":64,"storage_mb":50,"http_body_mb":8,"long_call_s":180},
		"schedules":[{"event":"t.p.tse_retry.tick","every_s":300,"jitter_s":60}],
		"db":{"migrations":"db/migrations"},
		"retention":{"keep_years":10,"reason_key":"plugin.tax_de.retention_reason"},
		"views_used":["sales.by_day.v1","items.top.v1"],
		"entries":[{"type":"page","key":"ask","label":"Ask","route":"/plugin/ai/ask","view":"ask.home","slot":"reports.panels"}]`)
	m, err := ParseManifest(strings.NewReader(src))
	if err != nil {
		t.Fatalf("valid ABI-3 fields refused: %v", err)
	}
	if m.WasmABI != 2 || m.Limits == nil || m.Limits.MemoryMB != 64 || m.Limits.LongCallS != 180 {
		t.Fatalf("wasm_abi/limits not parsed: %+v %+v", m.WasmABI, m.Limits)
	}
	if len(m.Schedules) != 1 || m.Schedules[0].Event != "t.p.tse_retry.tick" || m.Schedules[0].EveryS != 300 || m.Schedules[0].JitterS != 60 {
		t.Fatalf("schedules not parsed: %+v", m.Schedules)
	}
	if m.DB == nil || m.DB.Migrations != "db/migrations" {
		t.Fatalf("db not parsed: %+v", m.DB)
	}
	if m.Retention == nil || m.Retention.KeepYears != 10 || m.Retention.ReasonKey != "plugin.tax_de.retention_reason" {
		t.Fatalf("retention not parsed: %+v", m.Retention)
	}
	if len(m.ViewsUsed) != 2 {
		t.Fatalf("views_used not parsed: %+v", m.ViewsUsed)
	}
	if m.Entries[0].View != "ask.home" || m.Entries[0].Slot != "reports.panels" {
		t.Fatalf("entry view/slot not parsed: %+v", m.Entries[0])
	}
}

// A manifest without the new fields must marshal to exactly the bytes it
// did before them, or every signature already issued stops verifying.
func TestABI3Fields_OmittedFieldsKeepCanonicalBytes(t *testing.T) {
	m, err := ParseManifest(strings.NewReader(manifestWith(`,"entries":[{"type":"page","key":"p","label":"P","route":"/plugin/t/p"}]`)))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{"wasm_abi", "limits", "schedules", `"db"`, "retention", "views_used", `"view"`, `"slot"`} {
		if bytes.Contains(b, []byte(tag)) {
			t.Fatalf("absent field %s leaked into canonical bytes: %s", tag, b)
		}
	}
}

func TestParseManifest_ABI3Refusals(t *testing.T) {
	cases := []struct {
		name, extra, want string
	}{
		{"abi newer than this till", `,"wasm_abi":3`, "update the till"},
		{"abi far in the future", `,"wasm_abi":99`, "update the till"},
		{"abi negative", `,"wasm_abi":-1`, "is not a plugin ABI"},
		{"abi 1 never existed", `,"wasm_abi":1`, "is not a plugin ABI"},
		{"negative memory", `,"limits":{"memory_mb":-1}`, "limits.memory_mb"},
		{"negative storage", `,"limits":{"storage_mb":-5}`, "limits.storage_mb"},
		{"negative body", `,"limits":{"http_body_mb":-5}`, "limits.http_body_mb"},
		{"negative long call", `,"limits":{"long_call_s":-5}`, "limits.long_call_s"},
		{"schedule without event", `,"schedules":[{"every_s":60}]`, "schedules[0].event"},
		{"schedule event single segment", `,"schedules":[{"event":"tick","every_s":60}]`, "dot-separated lower-case segments"},
		{"schedule event upper case", `,"schedules":[{"event":"Tax.Tick","every_s":60}]`, "dot-separated lower-case segments"},
		{"schedule zero interval", `,"schedules":[{"event":"t.p.tick","every_s":0}]`, "every_s must be at least 30"},
		{"schedule under 30 s", `,"schedules":[{"event":"t.p.tick","every_s":29}]`, "every_s must be at least 30"},
		{"schedule jitter over interval", `,"schedules":[{"event":"t.p.tick","every_s":60,"jitter_s":61}]`, "jitter_s"},
		{"schedule raises a sale event", `,"schedules":[{"event":"sale.completed","every_s":60}]`, "core's \"sale\" namespace"},
		{"schedule raises a fiscal ask", `,"schedules":[{"event":"fiscal.sign.ask","every_s":60}]`, "core's \"fiscal\" namespace"},
		{"schedule raises a ui event", `,"schedules":[{"event":"ui.view.ask","every_s":60}]`, "core's \"ui\" namespace"},
		{"schedule raises a payment authorize", `,"schedules":[{"event":"payment.card.authorize","every_s":60}]`, "core's \"payment\" namespace"},
		{"schedule raises another plugin's event", `,"schedules":[{"event":"other.tick","every_s":60}]`, "must start with this plugin's own id (\"t.p.\")"},
		{"schedule shares only the id's first segment", `,"schedules":[{"event":"t.tick","every_s":60}]`, "must start with this plugin's own id"},
		{"schedule id prefix without a dot", `,"schedules":[{"event":"t.pq.tick","every_s":60}]`, "must start with this plugin's own id"},
		{"schedule negative jitter", `,"schedules":[{"event":"t.p.tick","every_s":60,"jitter_s":-1}]`, "jitter_s must be between"},
		{"db without migrations", `,"db":{}`, "db.migrations"},
		{"db absolute path", `,"db":{"migrations":"/etc/migrations"}`, "db.migrations"},
		{"db escapes plugin tree", `,"db":{"migrations":"../../core"}`, "db.migrations"},
		{"db escapes after clean", `,"db":{"migrations":"db/../../x"}`, "db.migrations"},
		{"db newline", `,"db":{"migrations":"db\nx"}`, "db.migrations"},
		{"db space", `,"db":{"migrations":"db x"}`, "db.migrations"},
		{"db windows absolute", `,"db":{"migrations":"C:\\migrations"}`, "db.migrations"},
		{"db backslash", `,"db":{"migrations":"db\\..\\..\\x"}`, "db.migrations"},
		{"retention negative", `,"retention":{"keep_years":-1}`, "retention.keep_years"},
		{"retention bad reason key", `,"retention":{"keep_years":1,"reason_key":"Has Spaces"}`, "retention.reason_key"},
		{"view name without version", `,"views_used":["sales.by_day"]`, "views_used"},
		{"view name upper case", `,"views_used":["Sales.by_day.v1"]`, "views_used"},
		{"view name duplicate", `,"views_used":["sales.by_day.v1","sales.by_day.v1"]`, "more than once"},
		{"entry unknown slot", `,"entries":[{"type":"page","key":"p","label":"P","route":"/plugin/t/p","slot":"sale.screen"}]`, "is not a content slot"},
		{"entry bad view", `,"entries":[{"type":"page","key":"p","label":"P","route":"/plugin/t/p","view":"<script>"}]`, "must be lower-case"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseManifest(strings.NewReader(manifestWith(tc.extra)))
			if err == nil {
				t.Fatalf("manifest %s accepted", tc.extra)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not name %q", err, tc.want)
			}
		})
	}
}

func TestParseManifest_ABI3AcceptsEdges(t *testing.T) {
	for _, extra := range []string{
		`,"wasm_abi":0`,
		`,"limits":{}`,
		`,"limits":{"memory_mb":100000}`, // over the ceiling: clamped, not refused
		`,"schedules":[{"event":"t.p.tick","every_s":30,"jitter_s":30}]`,
		`,"db":{"migrations":"./db/migrations"}`,
		`,"retention":{"keep_years":0}`,
		`,"views_used":["audit.summary.v12"]`,
	} {
		if _, err := ParseManifest(strings.NewReader(manifestWith(extra))); err != nil {
			t.Fatalf("%s refused: %v", extra, err)
		}
	}
	for _, slot := range contentSlots {
		extra := `,"entries":[{"type":"page","key":"p","label":"P","route":"/plugin/t/p","slot":"` + slot + `"}]`
		if _, err := ParseManifest(strings.NewReader(manifestWith(extra))); err != nil {
			t.Fatalf("slot %q refused: %v", slot, err)
		}
	}
}

// Real plugin ids use hyphens (com.universaltill.tax-de); the own-id
// prefix rule makes the id the event prefix, so the event format must
// accept every character an id may contain (ut-docs#3329).
func TestParseManifest_ScheduleEventWithHyphenatedID(t *testing.T) {
	src := `{"id":"com.universaltill.tax-de","name":"T","version":"1.0.0","runtime":"none",` +
		`"schedules":[{"event":"com.universaltill.tax-de.tse_retry.tick","every_s":300}]}`
	m, err := ParseManifest(strings.NewReader(src))
	if err != nil {
		t.Fatalf("hyphenated own-id schedule refused: %v", err)
	}
	if len(m.Schedules) != 1 || m.Schedules[0].Event != "com.universaltill.tax-de.tse_retry.tick" {
		t.Fatalf("schedules not parsed: %+v", m.Schedules)
	}
}

// ADR-0121 2026-10-03 amendment (ut-docs#3329): a plugin's event namespace
// is its manifest id, so an id may never start with a core event root.
func TestParseManifest_RefusesIDInCoreEventNamespace(t *testing.T) {
	for _, id := range []string{"sale", "sale.myplugin", "fiscal.de", "ui.x", "payment.card", "tax.de.tse"} {
		src := `{"id":"` + id + `","name":"T","version":"1.0.0","runtime":"none"}`
		_, err := ParseManifest(strings.NewReader(src))
		if err == nil {
			t.Fatalf("id %q in a core event namespace accepted", id)
		}
		root, _, _ := strings.Cut(id, ".")
		if !strings.Contains(err.Error(), "core event namespace") || !strings.Contains(err.Error(), `"`+root+`"`) {
			t.Fatalf("id %q: error %q does not name the core namespace %q", id, err, root)
		}
	}
	for _, id := range []string{"t.p", "com.universaltill.ut-faq", "tax_de", "sales_tools.x", "com.sale"} {
		src := `{"id":"` + id + `","name":"T","version":"1.0.0","runtime":"none"}`
		if _, err := ParseManifest(strings.NewReader(src)); err != nil {
			t.Fatalf("id %q refused: %v", id, err)
		}
	}
}

func TestContentSlots_AreADR0121Six(t *testing.T) {
	want := []string{"item.edit.actions", "reports.panels", "setup.wizard.steps", "eod.footer", "settings.sections", "admin.pages"}
	if strings.Join(contentSlots, ",") != strings.Join(want, ",") {
		t.Fatalf("contentSlots = %v, want %v", contentSlots, want)
	}
}

// The marketplace install path never goes through ParseManifest, so the
// verifier must refuse the same manifests.
func TestVerifyManifest_RefusesUnsupportedABI(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "plugin.json")
	src := `{"id":"t.p","name":"T","version":"1.0.0","runtime":"none","canonical_type":"theme","device_arch":"any","wasm_abi":3}`
	if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	mv, err := NewManifestVerifier("")
	if err != nil {
		t.Fatal(err)
	}
	_, err = mv.VerifyManifest(p)
	if err == nil || !strings.Contains(err.Error(), "update the till") {
		t.Fatalf("verifier accepted an ABI this till does not implement: %v", err)
	}
}

func TestEffectiveLimits_DefaultsAndCeilings(t *testing.T) {
	var none *Manifest = &Manifest{}
	got := none.EffectiveLimits("linux")
	want := ManifestLimits{MemoryMB: 64, StorageMB: 50, HTTPBodyMB: 8, LongCallS: 300}
	if got != want {
		t.Fatalf("defaults = %+v, want %+v", got, want)
	}

	huge := &Manifest{Limits: &ManifestLimits{MemoryMB: 9999, StorageMB: 999999, HTTPBodyMB: 9999, LongCallS: 9999}}
	if got, want := huge.EffectiveLimits("linux"), (ManifestLimits{MemoryMB: 256, StorageMB: 2048, HTTPBodyMB: 64, LongCallS: 300}); got != want {
		t.Fatalf("desktop ceilings = %+v, want %+v", got, want)
	}
	for _, goos := range []string{"android", "ios"} {
		if got, want := huge.EffectiveLimits(goos), (ManifestLimits{MemoryMB: 128, StorageMB: 512, HTTPBodyMB: 32, LongCallS: 300}); got != want {
			t.Fatalf("%s ceilings = %+v, want %+v", goos, got, want)
		}
	}

	small := &Manifest{Limits: &ManifestLimits{MemoryMB: 16, LongCallS: 30}}
	if got, want := small.EffectiveLimits("linux"), (ManifestLimits{MemoryMB: 16, StorageMB: 50, HTTPBodyMB: 8, LongCallS: 30}); got != want {
		t.Fatalf("requests under the ceiling = %+v, want %+v", got, want)
	}
	// Clamping must never write back into the signed manifest.
	if huge.Limits.MemoryMB != 9999 {
		t.Fatal("EffectiveLimits mutated the manifest")
	}
}

func TestEffectiveWasmABI(t *testing.T) {
	if got := (&Manifest{}).EffectiveWasmABI(); got != DefaultWasmABI {
		t.Fatalf("missing wasm_abi = %d, want %d", got, DefaultWasmABI)
	}
	if got := (&Manifest{WasmABI: 2}).EffectiveWasmABI(); got != 2 {
		t.Fatalf("wasm_abi 2 = %d", got)
	}
}
