package fiscal

import (
	"context"
	"errors"
	"testing"
)

// fakeCountryDocs is an in-memory CountryDocumentsReader. rows maps a
// normalised code to its stored shadow_customer_documents value; a missing
// code reads as "no row". calls counts every read, so a test can prove the
// builtin-forbidden path does no I/O.
type fakeCountryDocs struct {
	rows  map[string]string
	err   error
	calls int
	codes []string
}

func (f *fakeCountryDocs) ShadowCustomerDocuments(_ context.Context, code string) (string, bool, error) {
	f.calls++
	f.codes = append(f.codes, code)
	if f.err != nil {
		return "", false, f.err
	}
	v, ok := f.rows[code]
	return v, ok, nil
}

// builtinForbiddenFor stands in for data.BuiltinShadowDocumentsForbidden:
// the test names its own forbidden set, so this package's tests never
// depend on which real market ships as forbidden.
func builtinForbiddenFor(codes ...string) func(string) bool {
	return func(code string) bool {
		for _, c := range codes {
			if c == code {
				return true
			}
		}
		return false
	}
}

// ADR-0124 §2 step 2: a builtin-forbidden market is decided before any
// read — with no row, a pruned row (no row), or a stored "allowed" row —
// and the reader is never called.
func TestCustomerDocuments_BuiltinForbiddenIsSuppressedWithoutIO(t *testing.T) {
	ctx := context.Background()
	cases := map[string]*fakeCountryDocs{
		"no row":             {rows: map[string]string{}},
		"stored allowed row": {rows: map[string]string{"PT": "allowed"}},
		"read error":         {err: errors.New("disk I/O error")},
	}
	for name, reader := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := CustomerDocuments(ctx, "PT", builtinForbiddenFor("PT"), reader)
			if err != nil {
				t.Fatalf("err = %v, want nil (no read was needed)", err)
			}
			if got != DocumentsSuppressedShadow {
				t.Fatalf("decision = %v, want DocumentsSuppressedShadow", got)
			}
			if reader.calls != 0 {
				t.Fatalf("reader called %d times, want 0 — a builtin-forbidden market must be decided with no I/O", reader.calls)
			}
		})
	}
}

// ADR-0124 §2 step 1: the stored country is not normalised, so the gate
// normalises it itself before any lookup.
func TestCustomerDocuments_NormalisesTheCountryCode(t *testing.T) {
	ctx := context.Background()
	for _, in := range []string{" pt ", "pt", "Pt", "PT\n"} {
		reader := &fakeCountryDocs{}
		got, err := CustomerDocuments(ctx, in, builtinForbiddenFor("PT"), reader)
		if err != nil || got != DocumentsSuppressedShadow {
			t.Fatalf("CustomerDocuments(%q) = %v, %v; want DocumentsSuppressedShadow, nil", in, got, err)
		}
	}
	// The row read uses the normalised code too.
	reader := &fakeCountryDocs{rows: map[string]string{"XX": "forbidden"}}
	got, err := CustomerDocuments(ctx, " xx ", builtinForbiddenFor(), reader)
	if err != nil || got != DocumentsSuppressedShadow {
		t.Fatalf("stored-forbidden custom code: got %v, %v; want DocumentsSuppressedShadow, nil", got, err)
	}
	if len(reader.codes) != 1 || reader.codes[0] != "XX" {
		t.Fatalf("reader asked for %q, want [\"XX\"]", reader.codes)
	}
}

// ADR-0124 §2 step 3: for a market the builtins don't forbid, the stored
// row decides; no row means allowed.
func TestCustomerDocuments_StoredRowDecidesOtherMarkets(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		rows map[string]string
		want DocumentsDecision
	}{
		{"stored forbidden", map[string]string{"ZZ": "forbidden"}, DocumentsSuppressedShadow},
		{"stored allowed", map[string]string{"ZZ": "allowed"}, DocumentsAllowed},
		{"no row", map[string]string{}, DocumentsAllowed},
		{"unknown stored value", map[string]string{"ZZ": "maybe"}, DocumentsAllowed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := CustomerDocuments(ctx, "ZZ", builtinForbiddenFor("PT"), &fakeCountryDocs{rows: c.rows})
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if got != c.want {
				t.Fatalf("decision = %v, want %v", got, c.want)
			}
		})
	}
}

// ADR-0124 §2 step 4: a market the builtins don't forbid must never lose
// its receipts to an unreadable column it doesn't use — allowed, plus the
// error for the caller to log.
func TestCustomerDocuments_ReadErrorOnAllowedMarketIsAllowedWithError(t *testing.T) {
	readErr := errors.New("database is locked")
	got, err := CustomerDocuments(context.Background(), "DE", builtinForbiddenFor("PT"), &fakeCountryDocs{err: readErr})
	if got != DocumentsAllowed {
		t.Fatalf("decision = %v, want DocumentsAllowed", got)
	}
	if !errors.Is(err, readErr) {
		t.Fatalf("err = %v, want it to wrap %v", err, readErr)
	}
}

// ADR-0124 §2 "There is no lift": a forbidden market stays suppressed for a
// shop that has declared itself system of record AND confirmed a signing
// device. The tender gate sees that shop as fully set up; the documents
// gate must not. (CustomerDocuments takes no settings reader at all — this
// pins that the tender gate's view of the same shop is irrelevant here.)
func TestCustomerDocuments_SystemOfRecordAndPostureKeyDoNotLift(t *testing.T) {
	ctx := context.Background()
	shop := fakeSettings{vals: map[string]string{
		KeySystemOfRecord:                "true",
		SigningDeviceConfiguredKey("PT"): "true",
	}}
	if sor, err := IsSystemOfRecord(ctx, shop); err != nil || !sor {
		t.Fatalf("fixture: shop should read as system of record, got %v, %v", sor, err)
	}
	got, err := CustomerDocuments(ctx, "PT", builtinForbiddenFor("PT"), &fakeCountryDocs{rows: map[string]string{"PT": "allowed"}})
	if err != nil || got != DocumentsSuppressedShadow {
		t.Fatalf("got %v, %v; want DocumentsSuppressedShadow, nil", got, err)
	}
}

// A nil reader (no repository wired) must not panic and must not open a
// forbidden market; a nil builtin func falls through to the row.
func TestCustomerDocuments_NilDependencies(t *testing.T) {
	ctx := context.Background()
	if got, err := CustomerDocuments(ctx, "PT", builtinForbiddenFor("PT"), nil); err != nil || got != DocumentsSuppressedShadow {
		t.Fatalf("nil reader, builtin forbidden: got %v, %v", got, err)
	}
	if got, err := CustomerDocuments(ctx, "GB", nil, nil); err != nil || got != DocumentsAllowed {
		t.Fatalf("nil reader and builtins, other market: got %v, %v", got, err)
	}
	if got, err := CustomerDocuments(ctx, "ZZ", nil, &fakeCountryDocs{rows: map[string]string{"ZZ": "forbidden"}}); err != nil || got != DocumentsSuppressedShadow {
		t.Fatalf("nil builtins, stored forbidden: got %v, %v", got, err)
	}
}
