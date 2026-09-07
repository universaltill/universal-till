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
