package shoptemplate

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// memStore is an in-memory SettingsStore recording which writes were
// shop-wide and which per till.
type memStore struct {
	vals    map[string]string
	perTill map[string]bool
	getErr  error
	setErr  map[string]error
}

func newMemStore() *memStore {
	return &memStore{vals: map[string]string{}, perTill: map[string]bool{}, setErr: map[string]error{}}
}

func (m *memStore) Get(_ context.Context, k string) (string, bool, error) {
	if m.getErr != nil {
		return "", false, m.getErr
	}
	v, ok := m.vals[k]
	return v, ok, nil
}
func (m *memStore) Set(_ context.Context, k, v string) error {
	if err := m.setErr[k]; err != nil {
		return err
	}
	m.vals[k] = v
	return nil
}
func (m *memStore) SetPerTill(_ context.Context, k, v string) error {
	if err := m.setErr[k]; err != nil {
		return err
	}
	m.vals[k] = v
	m.perTill[k] = true
	return nil
}

func resolvedWith(t *testing.T, id string, seeds ...Seed) *Resolved {
	t.Helper()
	s := testSet(t, &Template{Schema: 1, ID: id, Version: 3, ShopTypes: []string{"retail"}, Seed: seeds})
	r := s.ForType("retail")
	if r == nil {
		t.Fatal("no resolved template")
	}
	return r
}

func TestApplyWritesUnsetSeedOnceAndRecordsLedger(t *testing.T) {
	st := newMemStore()
	r := resolvedWith(t, "retail", Seed{Key: "sale.order_type_prompt", Value: "off"}, Seed{Key: "theme", Value: "dark"})
	if err := Apply(t.Context(), st, r); err != nil {
		t.Fatal(err)
	}
	if got := st.vals["sale.order_type_prompt"]; got != "off" {
		t.Fatalf("sale.order_type_prompt = %q, want off", got)
	}
	if st.perTill["sale.order_type_prompt"] {
		t.Fatal("sale.order_type_prompt must be written shop-wide")
	}
	if got := st.vals["theme"]; got != "dark" || !st.perTill["theme"] {
		t.Fatalf("theme = %q perTill=%v, want dark written per till", got, st.perTill["theme"])
	}
	if got := st.vals["template.theme_seeded"]; got != "dark" || !st.perTill["template.theme_seeded"] {
		t.Fatalf("template.theme_seeded = %q perTill=%v, want dark per till", got, st.perTill["template.theme_seeded"])
	}
	var l Ledger
	if err := json.Unmarshal([]byte(st.vals[LedgerKey]), &l); err != nil {
		t.Fatalf("ledger: %v (%q)", err, st.vals[LedgerKey])
	}
	if l.ID != "retail" || l.Version != 3 || l.Seeded["sale.order_type_prompt"] != "off" || l.Seeded["theme"] != "dark" || l.At == "" {
		t.Fatalf("ledger = %+v, want id retail version 3 with both seeds and a timestamp", l)
	}
	if st.perTill[LedgerKey] {
		t.Fatal("shop.template_applied is shop-wide")
	}
}

func TestApplyNeverOverwritesExistingValue(t *testing.T) {
	st := newMemStore()
	st.vals["sale.order_type_prompt"] = "at_pay"
	r := resolvedWith(t, "retail", Seed{Key: "sale.order_type_prompt", Value: "off"})
	if err := Apply(t.Context(), st, r); err != nil {
		t.Fatal(err)
	}
	if got := st.vals["sale.order_type_prompt"]; got != "at_pay" {
		t.Fatalf("existing value overwritten: %q, want at_pay", got)
	}
	var l Ledger
	_ = json.Unmarshal([]byte(st.vals[LedgerKey]), &l)
	if _, ok := l.Seeded["sale.order_type_prompt"]; ok {
		t.Fatalf("ledger claims a key it did not seed: %+v", l)
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	st := newMemStore()
	r := resolvedWith(t, "retail", Seed{Key: "sale.order_type_prompt", Value: "off"})
	if err := Apply(t.Context(), st, r); err != nil {
		t.Fatal(err)
	}
	// The owner changes it back; a second Apply must not re-seed.
	st.vals["sale.order_type_prompt"] = ""
	delete(st.vals, "sale.order_type_prompt")
	first := st.vals[LedgerKey]
	if err := Apply(t.Context(), st, r); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.vals["sale.order_type_prompt"]; ok {
		t.Fatal("second Apply re-seeded a key")
	}
	if st.vals[LedgerKey] != first {
		t.Fatal("second Apply rewrote the ledger")
	}
}

func TestApplyNilResolvedIsNoOp(t *testing.T) {
	st := newMemStore()
	if err := Apply(t.Context(), st, nil); err != nil {
		t.Fatal(err)
	}
	if len(st.vals) != 0 {
		t.Fatalf("nil template wrote %v", st.vals)
	}
}

func TestApplyFailureLeavesNoLedgerSoItRetries(t *testing.T) {
	st := newMemStore()
	st.setErr["sale.order_type_prompt"] = errors.New("disk full")
	r := resolvedWith(t, "retail", Seed{Key: "sale.order_type_prompt", Value: "off"})
	if err := Apply(t.Context(), st, r); err == nil {
		t.Fatal("want the write error returned")
	}
	if _, ok := st.vals[LedgerKey]; ok {
		t.Fatal("ledger written after a failed seed; it would never retry")
	}
	delete(st.setErr, "sale.order_type_prompt")
	if err := Apply(t.Context(), st, r); err != nil {
		t.Fatal(err)
	}
	if st.vals["sale.order_type_prompt"] != "off" {
		t.Fatal("retry did not seed")
	}
}

func TestApplyReadErrorWritesNothing(t *testing.T) {
	st := newMemStore()
	st.getErr = errors.New("locked")
	r := resolvedWith(t, "retail", Seed{Key: "sale.order_type_prompt", Value: "off"})
	if err := Apply(t.Context(), st, r); err == nil {
		t.Fatal("want error")
	}
	if len(st.vals) != 0 {
		t.Fatalf("wrote %v despite a read error", st.vals)
	}
}

// A seed that bypassed validation (hand-built Resolved) is still refused at
// apply time: defence in depth for the closed whitelist.
func TestSeedWhitelistRejectsPaymentFiscalTaxAuthKeys(t *testing.T) {
	for _, key := range []string{
		"payments.default_method", "payment.x", "fiscal.mode", "tax.rate", "vat.id",
		"auth.pin_policy", "staff.pin", "cloud.token", "registration.stopped", "backup.x",
		"invoice.seller_name", "marketplace.token", "kiosk.payment_mode", "unknown.key",
	} {
		st := newMemStore()
		r := &Resolved{id: "x", version: 1, seeds: []Seed{{Key: key, Value: "off"}}}
		if err := Apply(t.Context(), st, r); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		if _, ok := st.vals[key]; ok {
			t.Fatalf("%s was seeded", key)
		}
		if !strings.Contains(st.vals[LedgerKey], `"seeded":{}`) {
			t.Fatalf("%s: ledger = %s, want empty seeded map", key, st.vals[LedgerKey])
		}
	}
}

// A seed whose registry row is not tier normal, or is absent from the
// registry, is refused twice over: the set will not build (so no Resolved
// carries it), and a hand-built Resolved that does is skipped by Apply and
// never written, however the key is spelled.
func TestSeedKeyNonNormalOrUnknownIsRefused(t *testing.T) {
	for _, key := range []string{"sale.sensitive", "sale.not_in_registry"} {
		_, err := Build([]*Template{{
			Schema: 1, ID: "x", Version: 1, ShopTypes: []string{"retail"}, LabelKey: "lbl", SummaryKey: "sum",
			Seed: []Seed{{Key: key, Value: "off"}},
		}}, newFakeCatalog(), false)
		if err == nil || !strings.Contains(err.Error(), "seed: key") {
			t.Fatalf("%s: Build err = %v, want a seed refusal", key, err)
		}

		st := newMemStore()
		if err := Apply(t.Context(), st, &Resolved{id: "x", version: 1, seeds: []Seed{{Key: key, Value: "off"}}}); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		if _, ok := st.vals[key]; ok {
			t.Fatalf("%s was seeded by Apply", key)
		}
		if !strings.Contains(st.vals[LedgerKey], `"seeded":{}`) {
			t.Fatalf("%s: ledger = %s, want empty seeded map", key, st.vals[LedgerKey])
		}
	}
	// A whitelisted key whose registry row is not normal is refused at build.
	cat := newFakeCatalog()
	delete(cat.normal, "sale.order_type_prompt")
	if _, err := Build([]*Template{{
		Schema: 1, ID: "x", Version: 1, ShopTypes: []string{"retail"}, LabelKey: "lbl", SummaryKey: "sum",
		Seed: []Seed{{Key: "sale.order_type_prompt", Value: "off"}},
	}}, cat, false); err == nil || !strings.Contains(err.Error(), "tier normal") {
		t.Fatalf("whitelisted key with a non-normal registry row: err = %v, want the tier-normal refusal", err)
	}
}

func TestApplyPerTillSeedsOnlyThemeAndMarker(t *testing.T) {
	st := newMemStore()
	r := resolvedWith(t, "retail", Seed{Key: "sale.order_type_prompt", Value: "off"}, Seed{Key: "theme", Value: "dark"})
	if err := ApplyPerTill(t.Context(), st, r); err != nil {
		t.Fatal(err)
	}
	if st.vals["theme"] != "dark" || !st.perTill["theme"] {
		t.Fatalf("theme = %q perTill=%v, want dark per till", st.vals["theme"], st.perTill["theme"])
	}
	if st.vals[ThemeSeededKey] != "dark" || !st.perTill[ThemeSeededKey] {
		t.Fatalf("marker = %q perTill=%v, want dark per till", st.vals[ThemeSeededKey], st.perTill[ThemeSeededKey])
	}
	if _, ok := st.vals["sale.order_type_prompt"]; ok {
		t.Fatal("per-till apply wrote a shop-wide key")
	}
	if _, ok := st.vals[LedgerKey]; ok {
		t.Fatal("per-till apply wrote the ledger")
	}
	if len(st.vals) != 2 {
		t.Fatalf("wrote %v, want exactly theme and its marker", st.vals)
	}
}

func TestApplyPerTillOnceAndNeverOverwrites(t *testing.T) {
	r := resolvedWith(t, "retail", Seed{Key: "theme", Value: "dark"})

	st := newMemStore()
	st.vals["theme"] = "light"
	if err := ApplyPerTill(t.Context(), st, r); err != nil {
		t.Fatal(err)
	}
	if st.vals["theme"] != "light" {
		t.Fatalf("existing theme overwritten: %q", st.vals["theme"])
	}

	st = newMemStore()
	if err := ApplyPerTill(t.Context(), st, r); err != nil {
		t.Fatal(err)
	}
	st.vals["theme"] = "high-contrast" // the cashier changed it afterwards
	if err := ApplyPerTill(t.Context(), st, r); err != nil {
		t.Fatal(err)
	}
	if st.vals["theme"] != "high-contrast" {
		t.Fatalf("second per-till apply re-seeded: %q", st.vals["theme"])
	}
}

func TestApplyPerTillNoThemeOrNilWritesNothing(t *testing.T) {
	st := newMemStore()
	if err := ApplyPerTill(t.Context(), st, nil); err != nil {
		t.Fatal(err)
	}
	r := resolvedWith(t, "retail", Seed{Key: "sale.order_type_prompt", Value: "off"})
	if err := ApplyPerTill(t.Context(), st, r); err != nil {
		t.Fatal(err)
	}
	if len(st.vals) != 0 {
		t.Fatalf("wrote %v with no theme seed", st.vals)
	}
}
