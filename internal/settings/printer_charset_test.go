package settings

import (
	"context"
	"testing"
)

// The reported ut-docs#1728 case: a German EUR till whose owner configured a
// printer at some point, so "utf8" is stored explicitly and the read-time
// default can never fire.
func TestAdoptDefaultPrinterCharset_MovesStoredUTF8ForEuroStore(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	mustSet(t, s, "setup.completed", "true")
	mustSet(t, s, "store.currency", "EUR")
	mustSet(t, s, "store.locale", "de-DE")
	mustSet(t, s, keyPrinterCharset, "utf8")

	from, to, changed, err := s.AdoptDefaultPrinterCharset(ctx)
	if err != nil || !changed {
		t.Fatalf("AdoptDefaultPrinterCharset = changed=%v err=%v, want changed=true err=nil", changed, err)
	}
	// The caller writes these into the audit log, so they have to be real.
	if from != "utf8" || to != "cp858" {
		t.Fatalf("reported from=%q to=%q, want utf8 -> cp858", from, to)
	}
	if got := mustGet(t, s, keyPrinterCharset); got != "cp858" {
		t.Fatalf("printer.charset = %q, want cp858", got)
	}

	// Runs exactly once: an operator who deliberately goes back to UTF-8
	// afterwards must not be overridden on the next boot.
	mustSet(t, s, keyPrinterCharset, "utf8")
	_, _, changed, err = s.AdoptDefaultPrinterCharset(ctx)
	if err != nil || changed {
		t.Fatalf("second run = changed=%v err=%v, want changed=false err=nil", changed, err)
	}
	if got := mustGet(t, s, keyPrinterCharset); got != "utf8" {
		t.Fatalf("printer.charset after a deliberate re-pick = %q, want it left as utf8", got)
	}
}

// ut-docs#1733 (independent review finding): a Greek/Croatian/... till that
// already completed a "v1" adoption pass under #1728 is frozen on "utf8"
// forever unless a version bump re-opens the question — v1 had nothing
// better to offer those locales, so every one of them is sitting on exactly
// the stored value this mechanism treats as "already decided." This is the
// concrete failure the versioned marker (documented since #1728, unused
// until now) exists to let a future pass fix.
func TestAdoptDefaultPrinterCharset_V1ToV2ReopensNewlyCoveredLocale(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	mustSet(t, s, "setup.completed", "true")
	mustSet(t, s, "store.currency", "EUR")
	mustSet(t, s, "store.locale", "el-GR")
	mustSet(t, s, keyPrinterCharset, "utf8")
	// Simulate a till that already ran the OLD (#1728) adoption pass: it
	// found nothing to move (win1253 didn't exist yet) but still spent the
	// one-shot marker at that version.
	mustSet(t, s, keyPrinterCharsetAdopted, "v1")

	from, to, changed, err := s.AdoptDefaultPrinterCharset(ctx)
	if err != nil || !changed {
		t.Fatalf("AdoptDefaultPrinterCharset = changed=%v err=%v, want changed=true err=nil", changed, err)
	}
	if from != "utf8" || to != "win1253" {
		t.Fatalf("reported from=%q to=%q, want utf8 -> win1253", from, to)
	}
	if got := mustGet(t, s, keyPrinterCharset); got != "win1253" {
		t.Fatalf("printer.charset = %q, want win1253", got)
	}
	if got := mustGet(t, s, keyPrinterCharsetAdopted); got != currentAdoptionVersion {
		t.Fatalf("adoption marker = %q, want it bumped to %q", got, currentAdoptionVersion)
	}

	// Runs exactly once per version, same as before: an operator who
	// deliberately goes back to UTF-8 after THIS version's pass must not be
	// overridden again at this same version.
	mustSet(t, s, keyPrinterCharset, "utf8")
	_, _, changed, err = s.AdoptDefaultPrinterCharset(ctx)
	if err != nil || changed {
		t.Fatalf("second run at same version = changed=%v err=%v, want changed=false err=nil", changed, err)
	}
	if got := mustGet(t, s, keyPrinterCharset); got != "utf8" {
		t.Fatalf("printer.charset after a deliberate re-pick = %q, want it left as utf8", got)
	}
}

// ut-docs#1775: the same reopening shape as v1->v2 above, one bump later — a
// Turkish EUR/GBP till that already completed a "v2" pass (which had nothing
// better than "utf8" to offer it, since win1254 didn't exist yet) is frozen
// on "utf8" forever unless the v3 bump reopens the question.
func TestAdoptDefaultPrinterCharset_V2ToV3ReopensTurkishLocale(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	mustSet(t, s, "setup.completed", "true")
	mustSet(t, s, "store.currency", "EUR")
	mustSet(t, s, "store.locale", "tr-TR")
	mustSet(t, s, keyPrinterCharset, "utf8")
	// Simulate a till that already ran the v2 (#1733) adoption pass: it found
	// nothing to move for Turkish (win1254 didn't exist yet) but still spent
	// the one-shot marker at that version.
	mustSet(t, s, keyPrinterCharsetAdopted, "v2")

	from, to, changed, err := s.AdoptDefaultPrinterCharset(ctx)
	if err != nil || !changed {
		t.Fatalf("AdoptDefaultPrinterCharset = changed=%v err=%v, want changed=true err=nil", changed, err)
	}
	if from != "utf8" || to != "win1254" {
		t.Fatalf("reported from=%q to=%q, want utf8 -> win1254", from, to)
	}
	if got := mustGet(t, s, keyPrinterCharset); got != "win1254" {
		t.Fatalf("printer.charset = %q, want win1254", got)
	}
	if got := mustGet(t, s, keyPrinterCharsetAdopted); got != currentAdoptionVersion {
		t.Fatalf("adoption marker = %q, want it bumped to %q", got, currentAdoptionVersion)
	}
}

// A TRY Turkish till must NOT be moved by the v3 bump — win1254 has no '₺'
// (ut-docs#1775), so TRY stays out of DefaultCharset's currency gate and the
// adoption pass has nothing better to offer this till than the "utf8" it's
// already on.
func TestAdoptDefaultPrinterCharset_V2ToV3DoesNotMoveTRYTill(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	mustSet(t, s, "setup.completed", "true")
	mustSet(t, s, "store.currency", "TRY")
	mustSet(t, s, "store.locale", "tr-TR")
	mustSet(t, s, keyPrinterCharset, "utf8")
	mustSet(t, s, keyPrinterCharsetAdopted, "v2")

	_, _, changed, err := s.AdoptDefaultPrinterCharset(ctx)
	if err != nil || changed {
		t.Fatalf("AdoptDefaultPrinterCharset = changed=%v err=%v, want changed=false err=nil", changed, err)
	}
	if got := mustGet(t, s, keyPrinterCharset); got != "utf8" {
		t.Fatalf("printer.charset = %q, want it left as utf8", got)
	}
	if got := mustGet(t, s, keyPrinterCharsetAdopted); got != currentAdoptionVersion {
		t.Fatalf("adoption marker = %q, want it bumped to %q even though nothing moved", got, currentAdoptionVersion)
	}
}

func TestAdoptDefaultPrinterCharset_LeavesExplicitChoicesAlone(t *testing.T) {
	ctx := context.Background()
	for _, explicit := range []string{"ascii", "cp858"} {
		t.Run(explicit, func(t *testing.T) {
			s := newTestStore(t)
			mustSet(t, s, "setup.completed", "true")
			mustSet(t, s, "store.currency", "EUR")
			mustSet(t, s, "store.locale", "de-DE")
			mustSet(t, s, keyPrinterCharset, explicit)

			_, _, changed, err := s.AdoptDefaultPrinterCharset(ctx)
			if err != nil || changed {
				t.Fatalf("changed=%v err=%v, want changed=false err=nil", changed, err)
			}
			if got := mustGet(t, s, keyPrinterCharset); got != explicit {
				t.Fatalf("printer.charset = %q, want the operator's own %q", got, explicit)
			}
		})
	}
}

// CP858 has no 'ı'/'ş', so a Turkish till must not be dragged onto it.
func TestAdoptDefaultPrinterCharset_DoesNotSwitchTurkishStore(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	mustSet(t, s, "setup.completed", "true")
	mustSet(t, s, "store.currency", "TRY")
	mustSet(t, s, "store.locale", "tr-TR")
	mustSet(t, s, keyPrinterCharset, "utf8")

	_, _, changed, err := s.AdoptDefaultPrinterCharset(ctx)
	if err != nil || changed {
		t.Fatalf("changed=%v err=%v, want changed=false err=nil", changed, err)
	}
	if got := mustGet(t, s, keyPrinterCharset); got != "utf8" {
		t.Fatalf("printer.charset = %q, want utf8 left in place for a Turkish store", got)
	}
}

// A fresh install boots before the setup wizard picks a country — but the
// schema SEEDS store.currency = 'GBP' and boot writes store.locale = "en-US"
// from the config default, so the placeholder state this must defer through
// is NOT an empty currency. An earlier draft checked for one; the independent
// review showed that check could never fire, and the pass would therefore
// burn its single-shot marker at first boot on seeded data. This test
// reproduces the real first-boot state rather than a manufactured one.
func TestAdoptDefaultPrinterCharset_DefersUntilSetupCompletes(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	// Exactly what 001_init.sql + SaveRuntimeConfig leave behind on boot 1.
	mustSet(t, s, "store.currency", "GBP")
	mustSet(t, s, "store.locale", "en-US")
	mustSet(t, s, keyPrinterCharset, "utf8")

	_, _, changed, err := s.AdoptDefaultPrinterCharset(ctx)
	if err != nil || changed {
		t.Fatalf("changed=%v err=%v, want changed=false err=nil before setup", changed, err)
	}
	if v, _, _ := s.Get(ctx, keyPrinterCharsetAdopted); v != "" {
		t.Fatalf("must NOT spend the one-shot marker before setup completes, got %q", v)
	}

	// The shop completes setup as a German EUR store; the next boot does the
	// real work. This is the sequence the review showed was broken before.
	mustSet(t, s, "setup.completed", "true")
	mustSet(t, s, "store.currency", "EUR")
	mustSet(t, s, "store.locale", "de-DE")
	_, _, changed, err = s.AdoptDefaultPrinterCharset(ctx)
	if err != nil || !changed {
		t.Fatalf("after setup: changed=%v err=%v, want changed=true err=nil", changed, err)
	}
	if got := mustGet(t, s, keyPrinterCharset); got != "cp858" {
		t.Fatalf("printer.charset = %q, want cp858", got)
	}
}

// Nothing stored: printerConfig's read-time default already covers this till,
// and writing a row would turn a live default into a frozen one.
func TestAdoptDefaultPrinterCharset_LeavesUnsetCharsetUnset(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	mustSet(t, s, "setup.completed", "true")
	mustSet(t, s, "store.currency", "EUR")
	mustSet(t, s, "store.locale", "de-DE")

	if _, _, _, err := s.AdoptDefaultPrinterCharset(ctx); err != nil {
		t.Fatalf("AdoptDefaultPrinterCharset: %v", err)
	}
	if v, ok, _ := s.Get(ctx, keyPrinterCharset); ok && v != "" {
		t.Fatalf("expected printer.charset left unset, got %q", v)
	}
}

func mustSet(t *testing.T, s *Store, k, v string) {
	t.Helper()
	if err := s.Set(context.Background(), k, v); err != nil {
		t.Fatalf("Set(%q): %v", k, err)
	}
}

func mustGet(t *testing.T, s *Store, k string) string {
	t.Helper()
	v, _, err := s.Get(context.Background(), k)
	if err != nil {
		t.Fatalf("Get(%q): %v", k, err)
	}
	return v
}
