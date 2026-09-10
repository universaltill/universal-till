package main

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/universaltill/universal-till/internal/manual"
)

func TestComputeSignature(t *testing.T) {
	tests := []struct {
		name string
		md   string
		want signature
	}{
		{
			name: "empty",
			md:   "",
			want: signature{},
		},
		{
			name: "headings",
			md:   "# Title\n\n## Section one\n\n### Sub\n",
			want: signature{Headings: 3},
		},
		{
			name: "numbered steps and a nested bullet",
			md: "1. First step\n" +
				"   - a detail\n" +
				"2. Second step\n",
			want: signature{NumberedStep: 2, SubBullets: 1},
		},
		{
			name: "top-level bullets under a heading",
			md: "## Good to know\n\n" +
				"- one\n" +
				"- two\n",
			want: signature{Headings: 1, TopBullets: 2},
		},
		{
			name: "bold lead-ins on numbered, top-level and nested bullets alike",
			md: "1. **Step label:** body text\n" +
				"   - **Nested label:** detail\n" +
				"- **Top label:** other detail\n" +
				"- plain bullet with no bold lead-in\n",
			want: signature{NumberedStep: 1, SubBullets: 1, TopBullets: 2, BoldLeadins: 3},
		},
		{
			name: "asterisk bullets count the same as hyphen bullets",
			md:   "* one\n  * nested\n",
			want: signature{TopBullets: 1, SubBullets: 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeSignature(tt.md)
			if got != tt.want {
				t.Errorf("computeSignature(%q) = %+v, want %+v", tt.md, got, tt.want)
			}
		})
	}
}

func TestLoadBaselineMissingFileIsEmptyNotError(t *testing.T) {
	got, err := loadBaseline(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatalf("loadBaseline: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("loadBaseline of a missing file = %v, want empty", got)
	}
}

func TestLoadBaselineParsesEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "baseline.json")
	const content = `{
  "entries": [
    {
      "locale": "de",
      "topic": "catalog",
      "signature": {"headings": 3, "numbered_steps": 7, "sub_bullets": 11, "top_bullets": 3, "bold_leadins": 11},
      "reason": "ut-docs#1962 pre-existing drift"
    }
  ]
}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := loadBaseline(path)
	if err != nil {
		t.Fatalf("loadBaseline: %v", err)
	}
	entry, ok := got[[2]string{"de", "catalog"}]
	if !ok {
		t.Fatalf("loadBaseline missing the de/catalog entry: %v", got)
	}
	want := signature{Headings: 3, NumberedStep: 7, SubBullets: 11, TopBullets: 3, BoldLeadins: 11}
	if entry.Signature != want {
		t.Errorf("entry.Signature = %+v, want %+v", entry.Signature, want)
	}
	if entry.Reason != "ut-docs#1962 pre-existing drift" {
		t.Errorf("entry.Reason = %q", entry.Reason)
	}
}

func TestLoadBaselineRejectsDuplicateEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "baseline.json")
	const content = `{
  "entries": [
    {"locale": "de", "topic": "catalog", "signature": {}, "reason": "a"},
    {"locale": "de", "topic": "catalog", "signature": {}, "reason": "b"}
  ]
}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := loadBaseline(path); err == nil {
		t.Fatal("loadBaseline of a file with a duplicate locale/topic entry: want an error, got nil")
	}
}

// topicFile builds one web/help/<locale>/<id>.md fixture file's bytes.
func topicFile(id, title, body string) []byte {
	return []byte("---\nid: " + id + "\ntitle: " + title + "\nsection: Test\norder: 1\n---\n\n" + body)
}

func loadFixtureLibrary(t *testing.T, files map[string][]byte) *manual.Library {
	t.Helper()
	mapFS := fstest.MapFS{}
	for path, data := range files {
		mapFS[path] = &fstest.MapFile{Data: data}
	}
	lib, err := manual.Load(mapFS, "help")
	if err != nil {
		t.Fatalf("manual.Load: %v", err)
	}
	return lib
}

func TestCheckAllNoDriftIsClean(t *testing.T) {
	lib := loadFixtureLibrary(t, map[string][]byte{
		"help/en/x.md": topicFile("x", "X", "# X\n\n1. Step one\n"),
		"help/de/x.md": topicFile("x", "X (de)", "# X (de)\n\n1. Schritt eins\n"),
	})

	findings := checkAll(lib, map[[2]string]baselineEntry{})
	for _, f := range findings {
		if f.fail {
			t.Errorf("unexpected failing finding with no real drift: %s", f.msg)
		}
	}
}

func TestCheckAllUnbaselinedDriftFails(t *testing.T) {
	lib := loadFixtureLibrary(t, map[string][]byte{
		"help/en/x.md": topicFile("x", "X", "# X\n\n1. Step one\n2. Step two\n"),
		"help/de/x.md": topicFile("x", "X (de)", "# X (de)\n\n1. Nur ein Schritt\n"),
	})

	findings := checkAll(lib, map[[2]string]baselineEntry{})
	if !anyFail(findings) {
		t.Errorf("drifted topic with no baseline entry should fail; findings: %+v", findings)
	}
}

func TestCheckAllKnownBaselinedDriftPasses(t *testing.T) {
	lib := loadFixtureLibrary(t, map[string][]byte{
		"help/en/x.md": topicFile("x", "X", "# X\n\n1. Step one\n2. Step two\n"),
		"help/de/x.md": topicFile("x", "X (de)", "# X (de)\n\n1. Nur ein Schritt\n"),
	})
	baseline := map[[2]string]baselineEntry{
		{"de", "x"}: {
			Locale:    "de",
			Topic:     "x",
			Signature: signature{Headings: 1, NumberedStep: 1},
			English:   signature{Headings: 1, NumberedStep: 2},
			Reason:    "test fixture",
		},
	}

	findings := checkAll(lib, baseline)
	if anyFail(findings) {
		t.Errorf("drift matching its baseline entry exactly (both sides anchored) should pass; findings: %+v", findings)
	}
}

func TestCheckAllEnglishMovedSinceBaselineFails(t *testing.T) {
	// The locale side is exactly what the baseline recorded, but English
	// has grown further since — this is the hole an English-blind baseline
	// used to leave open (independent review finding, ut-docs#1962): a
	// baselined pair must still notice English moving, not just the
	// locale staying frozen.
	lib := loadFixtureLibrary(t, map[string][]byte{
		"help/en/x.md": topicFile("x", "X", "# X\n\n1. Step one\n2. Step two\n3. Step three\n"),
		"help/de/x.md": topicFile("x", "X (de)", "# X (de)\n\n1. Nur ein Schritt\n"),
	})
	baseline := map[[2]string]baselineEntry{
		// Recorded when English only had 2 steps; English now has 3.
		{"de", "x"}: {
			Locale:    "de",
			Topic:     "x",
			Signature: signature{Headings: 1, NumberedStep: 1},
			English:   signature{Headings: 1, NumberedStep: 2},
			Reason:    "test fixture",
		},
	}

	findings := checkAll(lib, baseline)
	if !anyFail(findings) {
		t.Error("a baseline entry whose recorded English signature no longer matches English's current signature should fail")
	}
}

func TestCheckAllStaleBaselineFailsOnceDriftIsFixed(t *testing.T) {
	// English and German now match structurally, but the baseline still
	// claims a mismatch — that entry is stale and must be removed.
	lib := loadFixtureLibrary(t, map[string][]byte{
		"help/en/x.md": topicFile("x", "X", "# X\n\n1. Step one\n"),
		"help/de/x.md": topicFile("x", "X (de)", "# X (de)\n\n1. Schritt eins\n"),
	})
	baseline := map[[2]string]baselineEntry{
		{"de", "x"}: {Locale: "de", Topic: "x", Signature: signature{NumberedStep: 0}, Reason: "stale"},
	}

	findings := checkAll(lib, baseline)
	if !anyFail(findings) {
		t.Error("a baseline entry for drift that no longer exists should fail (stale entry)")
	}
}

func TestCheckAllBaselineDriftedFurtherFails(t *testing.T) {
	// The baseline recorded one mismatch, but the real one has since
	// changed (got worse, or just different) — must fail, not silently
	// widen the exemption.
	lib := loadFixtureLibrary(t, map[string][]byte{
		"help/en/x.md": topicFile("x", "X", "# X\n\n1. Step one\n2. Step two\n"),
		"help/de/x.md": topicFile("x", "X (de)", "# X (de)\n\n1. Nur ein Schritt\n"),
	})
	baseline := map[[2]string]baselineEntry{
		// Baseline claims 2 numbered steps in German (i.e. no drift) but
		// the fixture actually only has 1 — mismatch against the baseline
		// itself, not just against English.
		{"de", "x"}: {Locale: "de", Topic: "x", Signature: signature{NumberedStep: 2}, Reason: "wrong"},
	}

	findings := checkAll(lib, baseline)
	if !anyFail(findings) {
		t.Error("a baseline entry whose recorded signature no longer matches reality should fail")
	}
}

func TestCheckAllBaselineEntryForUncheckedTopicFails(t *testing.T) {
	lib := loadFixtureLibrary(t, map[string][]byte{
		"help/en/x.md": topicFile("x", "X", "# X\n\n1. Step one\n"),
		"help/de/x.md": topicFile("x", "X (de)", "# X (de)\n\n1. Schritt eins\n"),
	})
	baseline := map[[2]string]baselineEntry{
		{"de", "does-not-exist"}: {Locale: "de", Topic: "does-not-exist", Signature: signature{}, Reason: "orphaned"},
	}

	findings := checkAll(lib, baseline)
	if !anyFail(findings) {
		t.Error("a baseline entry naming a locale/topic pair that was never checked should fail")
	}
}

func TestCheckAllSkipsUntranslatedLocaleFallback(t *testing.T) {
	// German has no x.md at all, so manual.Topic falls back to English and
	// reports Translated=false — guard-help-topics.sh's job, not this
	// guard's; it must not also complain here.
	lib := loadFixtureLibrary(t, map[string][]byte{
		"help/en/x.md": topicFile("x", "X", "# X\n\n1. Step one\n2. Step two\n"),
	})

	findings := checkAll(lib, map[[2]string]baselineEntry{})
	if anyFail(findings) {
		t.Errorf("a locale with no file at all for this topic should be silently skipped; findings: %+v", findings)
	}
}

func TestRefreshBaselineDropsFixedAndDeletedEntries(t *testing.T) {
	lib := loadFixtureLibrary(t, map[string][]byte{
		"help/en/still-drifted.md": topicFile("still-drifted", "Still drifted", "# T\n\n1. One\n2. Two\n"),
		"help/de/still-drifted.md": topicFile("still-drifted", "T (de)", "# T (de)\n\n1. Eins\n"),
		"help/en/now-fixed.md":     topicFile("now-fixed", "Now fixed", "# T\n\n1. One\n"),
		"help/de/now-fixed.md":     topicFile("now-fixed", "T (de)", "# T (de)\n\n1. Eins\n"),
	})
	baseline := map[[2]string]baselineEntry{
		{"de", "still-drifted"}: {
			Locale: "de", Topic: "still-drifted",
			Signature: signature{Headings: 1, NumberedStep: 999}, // stale count, should be refreshed
			English:   signature{Headings: 1, NumberedStep: 999}, // stale count, should be refreshed
			Reason:    "keep me, just refresh my counts",
		},
		{"de", "now-fixed"}: {
			Locale: "de", Topic: "now-fixed",
			Signature: signature{NumberedStep: 0},
			Reason:    "translation was fixed since this was recorded — should be dropped",
		},
		{"de", "deleted-topic"}: {
			Locale: "de", Topic: "deleted-topic",
			Signature: signature{},
			Reason:    "topic no longer exists — should be dropped",
		},
	}

	got := refreshBaseline(lib, baseline)

	if len(got) != 1 {
		t.Fatalf("refreshBaseline kept %d entries, want 1 (still-drifted only): %+v", len(got), got)
	}
	entry, ok := got[[2]string{"de", "still-drifted"}]
	if !ok {
		t.Fatal("refreshBaseline dropped the still-genuinely-drifted entry")
	}
	wantSig := signature{Headings: 1, NumberedStep: 1}
	wantEn := signature{Headings: 1, NumberedStep: 2}
	if entry.Signature != wantSig || entry.English != wantEn {
		t.Errorf("refreshBaseline signature/english = %+v/%+v, want %+v/%+v", entry.Signature, entry.English, wantSig, wantEn)
	}
	if entry.Reason != "keep me, just refresh my counts" {
		t.Errorf("refreshBaseline changed Reason to %q, want it preserved", entry.Reason)
	}
}

func TestRefreshBaselineNeverAddsNewEntries(t *testing.T) {
	// Real drift exists (en/de differ), but there's no baseline entry for
	// it — refreshBaseline must not invent one; that needs a human-picked
	// Reason.
	lib := loadFixtureLibrary(t, map[string][]byte{
		"help/en/x.md": topicFile("x", "X", "# X\n\n1. One\n2. Two\n"),
		"help/de/x.md": topicFile("x", "X (de)", "# X (de)\n\n1. Eins\n"),
	})

	got := refreshBaseline(lib, map[[2]string]baselineEntry{})
	if len(got) != 0 {
		t.Errorf("refreshBaseline invented %d new entries with no prior baseline: %+v", len(got), got)
	}
}

func TestWriteBaselineRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "baseline.json")
	entries := map[[2]string]baselineEntry{
		{"de", "b-topic"}: {Locale: "de", Topic: "b-topic", Signature: signature{Headings: 2}, English: signature{Headings: 3}, Reason: "b"},
		{"ar", "a-topic"}: {Locale: "ar", Topic: "a-topic", Signature: signature{Headings: 1}, English: signature{Headings: 2}, Reason: "a"},
	}

	if err := writeBaseline(path, entries); err != nil {
		t.Fatalf("writeBaseline: %v", err)
	}

	got, err := loadBaseline(path)
	if err != nil {
		t.Fatalf("loadBaseline after writeBaseline: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("round-tripped %d entries, want 2", len(got))
	}
	for key, want := range entries {
		if got[key] != want {
			t.Errorf("round-tripped entry %v = %+v, want %+v", key, got[key], want)
		}
	}

	// Deterministic ordering (locale then topic) so a refresh's diff stays
	// minimal and reviewable.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	arIdx := indexOf(t, string(raw), `"locale": "ar"`)
	deIdx := indexOf(t, string(raw), `"locale": "de"`)
	if arIdx > deIdx {
		t.Errorf("writeBaseline did not sort entries by locale: ar at %d, de at %d", arIdx, deIdx)
	}
}

func indexOf(t *testing.T, haystack, needle string) int {
	t.Helper()
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	t.Fatalf("substring %q not found in %q", needle, haystack)
	return -1
}

func anyFail(findings []finding) bool {
	for _, f := range findings {
		if f.fail {
			return true
		}
	}
	return false
}
