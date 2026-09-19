package pos

import (
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/money"
)

// newTestTableSessions builds a TableSessions whose factory mints engines
// over a tiny fixed catalog — the same mapResolver shape
// TestServiceConcurrentMutations (concurrency_test.go) uses.
func newTestTableSessions(idleTTL time.Duration) *TableSessions {
	return NewTableSessions(func() *Service {
		return NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, mapResolver{
			"A": {SKU: "A", Name: "Item A", PriceCents: money.FromMinor(100)},
			"B": {SKU: "B", Name: "Item B", PriceCents: money.FromMinor(250)},
		})
	}, idleTTL)
}

// ut-docs#2261 (a): two goroutines hammering DIFFERENT tables through
// SessionForTable + basket mutations must never cross-contaminate — each
// table's basket only ever holds that table's own lines. Run under -race.
func TestTableSessions_DifferentTablesNeverCrossContaminate(t *testing.T) {
	ts := newTestTableSessions(time.Hour)
	const iters = 500
	var wg sync.WaitGroup
	for _, tc := range []struct{ table, code string }{{"tbl-1", "A"}, {"tbl-2", "B"}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				_, e, _, err := ts.SessionForTable(tc.table)
				if err != nil {
					t.Errorf("SessionForTable(%s): %v", tc.table, err)
					return
				}
				if _, err := e.Scan(tc.code); err != nil {
					t.Errorf("Scan(%s): %v", tc.code, err)
					return
				}
				for _, l := range e.Lines() {
					if l.SKU != tc.code {
						t.Errorf("table %s basket holds foreign line %q", tc.table, l.SKU)
						return
					}
				}
			}
		}()
	}
	wg.Wait()

	_, e1, _, _ := ts.SessionForTable("tbl-1")
	_, e2, _, _ := ts.SessionForTable("tbl-2")
	if e1 == e2 {
		t.Fatal("two tables resolved to the same engine instance")
	}
	for _, c := range []struct {
		e    *Service
		code string
	}{{e1, "A"}, {e2, "B"}} {
		lines := c.e.Lines()
		if len(lines) != 1 || lines[0].SKU != c.code || lines[0].Qty != iters {
			t.Fatalf("table basket for %s: got %+v, want one line %s x%d", c.code, lines, c.code, iters)
		}
	}
}

// ut-docs#2261 (b): two goroutines hitting the SAME table (two devices at
// one table) both resolve to the SAME engine and neither mutation is lost.
func TestTableSessions_SameTableSharesOneEngineNoLostUpdate(t *testing.T) {
	ts := newTestTableSessions(time.Hour)
	const iters = 500
	var wg sync.WaitGroup
	engines := make([]*Service, 2)
	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				_, e, _, err := ts.SessionForTable("tbl-7")
				if err != nil {
					t.Errorf("SessionForTable: %v", err)
					return
				}
				engines[g] = e
				if _, err := e.Scan("A"); err != nil {
					t.Errorf("Scan: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if engines[0] != engines[1] {
		t.Fatal("two devices at one table resolved to different engines")
	}
	lines := engines[0].Lines()
	if len(lines) != 1 || lines[0].Qty != 2*iters {
		t.Fatalf("shared basket: got %+v, want one line A x%d (no lost update)", lines, 2*iters)
	}
}

// ut-docs#2261 (c): Sweep evicts an idle session and leaves a fresh one.
func TestTableSessions_SweepEvictsIdleNotFresh(t *testing.T) {
	ts := newTestTableSessions(time.Hour)
	base := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	ts.now = func() time.Time { return base }
	idleTok, _, _, err := ts.SessionForTable("idle")
	if err != nil {
		t.Fatal(err)
	}
	ts.now = func() time.Time { return base.Add(59 * time.Minute) }
	freshTok, _, _, err := ts.SessionForTable("fresh")
	if err != nil {
		t.Fatal(err)
	}

	if n := ts.Sweep(base.Add(61 * time.Minute)); n != 1 {
		t.Fatalf("Sweep evicted %d sessions, want 1", n)
	}
	if _, _, ok := ts.Lookup(idleTok); ok {
		t.Fatal("idle session still resolvable after Sweep")
	}
	if _, tableID, ok := ts.Lookup(freshTok); !ok || tableID != "fresh" {
		t.Fatalf("fresh session lost: ok=%v table=%q", ok, tableID)
	}
	// The evicted table's next visit mints a NEW session, not the old one.
	tok2, _, existed, err := ts.SessionForTable("idle")
	if err != nil {
		t.Fatal(err)
	}
	if existed || tok2 == idleTok {
		t.Fatalf("evicted table re-joined the old session (existed=%v, sameToken=%v)", existed, tok2 == idleTok)
	}
}

// ut-docs#2261 (d): a still-live table joins its existing session — same
// token both times, existed=true on the second call.
func TestTableSessions_SameLiveTableReturnsSameToken(t *testing.T) {
	ts := newTestTableSessions(time.Hour)
	tok1, e1, existed1, err := ts.SessionForTable("tbl-3")
	if err != nil {
		t.Fatal(err)
	}
	if existed1 {
		t.Fatal("first visit reported existed=true")
	}
	if len(tok1) != 32 {
		t.Fatalf("token %q: want 16 crypto/rand bytes hex-encoded (32 chars)", tok1)
	}
	tok2, e2, existed2, err := ts.SessionForTable("tbl-3")
	if err != nil {
		t.Fatal(err)
	}
	if !existed2 || tok2 != tok1 || e2 != e1 {
		t.Fatalf("second visit: existed=%v tok2==tok1=%v same engine=%v — want a join, not a duplicate", existed2, tok2 == tok1, e2 == e1)
	}
	if e, tableID, ok := ts.Lookup(tok1); !ok || e != e1 || tableID != "tbl-3" {
		t.Fatalf("Lookup(tok1): ok=%v sameEngine=%v table=%q", ok, e == e1, tableID)
	}
	if _, _, ok := ts.Lookup("not-a-token"); ok {
		t.Fatal("unknown token resolved")
	}
	if _, _, ok := ts.Lookup(""); ok {
		t.Fatal("empty token resolved")
	}
}

// Lookup touches lastActivity: a session that is being used must not be
// swept just because it was CREATED long ago.
func TestTableSessions_LookupTouchesActivity(t *testing.T) {
	ts := newTestTableSessions(time.Hour)
	base := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	ts.now = func() time.Time { return base }
	tok, _, _, err := ts.SessionForTable("tbl-9")
	if err != nil {
		t.Fatal(err)
	}
	ts.now = func() time.Time { return base.Add(50 * time.Minute) }
	if _, _, ok := ts.Lookup(tok); !ok {
		t.Fatal("Lookup miss on a live token")
	}
	if n := ts.Sweep(base.Add(90 * time.Minute)); n != 0 {
		t.Fatalf("Sweep evicted %d, want 0 — Lookup 40 minutes ago should have kept it alive", n)
	}
	// Idle-expired on Lookup itself: a token whose session has gone past
	// the TTL but not yet been swept must read as "no session".
	if _, _, ok := ts.Lookup(tok); !ok {
		t.Fatal("precondition: token live")
	}
	ts.now = func() time.Time { return base.Add(3 * time.Hour) }
	if _, _, ok := ts.Lookup(tok); ok {
		t.Fatal("Lookup resolved an idle-expired session before Sweep ran")
	}
}

// SetConfigAll / AnyHasItems / All: the broadcast + basket-guard + scan
// helpers the peripheral call sites use (settings/setup, auto-update,
// catalog cleanup). A nil *TableSessions is a safe no-op for every one, so
// bare-Deps test harnesses that never wire KioskSessions stay valid — same
// convention as the existing `d.KioskEngine != nil` guards.
func TestTableSessions_BroadcastGuardAndSnapshot(t *testing.T) {
	var nilTS *TableSessions
	nilTS.SetConfigAll(Config{})
	if nilTS.AnyHasItems() || nilTS.All() != nil || nilTS.Sweep(time.Now()) != 0 {
		t.Fatal("nil TableSessions must be an inert no-op")
	}
	if _, _, ok := nilTS.Lookup("x"); ok {
		t.Fatal("nil TableSessions Lookup must miss")
	}

	ts := newTestTableSessions(time.Hour)
	if ts.AnyHasItems() {
		t.Fatal("AnyHasItems on an empty store")
	}
	_, e1, _, _ := ts.SessionForTable("t1")
	_, e2, _, _ := ts.SessionForTable("t2")
	if ts.AnyHasItems() {
		t.Fatal("AnyHasItems with two empty baskets")
	}
	if _, err := e2.Scan("A"); err != nil {
		t.Fatal(err)
	}
	if !ts.AnyHasItems() {
		t.Fatal("AnyHasItems missed a basket with a line")
	}
	all := ts.All()
	if len(all) != 2 || !((all[0] == e1 && all[1] == e2) || (all[0] == e2 && all[1] == e1)) {
		t.Fatalf("All() = %v, want exactly {e1, e2}", all)
	}
	cfg := Config{TaxInclusive: true, TaxRateBasisPoints: 700}
	ts.SetConfigAll(cfg)
	if e1.Config() != cfg || e2.Config() != cfg {
		t.Fatalf("SetConfigAll did not reach every engine: %+v / %+v", e1.Config(), e2.Config())
	}
}
