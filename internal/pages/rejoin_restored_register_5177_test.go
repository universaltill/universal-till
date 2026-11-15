package pages

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
)

// ut-docs#5177: a restored copy of a joined till keeps selling offline under
// the ORIGINAL till's sync.till_register_id. When it re-joins a main till, the
// entries it made after the restore must not carry that register to the main
// till (wrong stock location, wrong register on no-sale opens and cleared
// baskets): they are sent under the register the new main till provisioned for
// this till. What the original made before the clone keeps its register.

const (
	origRegister = "reg-original"
	newRegister  = "reg-provisioned"
	restoredAt   = "2026-10-29T09:00:00Z"
)

// markRestoredCopy makes the till a restored copy as clearClonedIdentity leaves
// it: no bearer, provisional receipt prefix, restore instant.
func markRestoredCopy(t *testing.T, s *rejoinShop) {
	t.Helper()
	for k, v := range map[string]string{
		"sync.bearer": "", "sync.till_id": "", "sync.receipt_prefix": "R1a2b-",
		"sync.restored_since": restoredAt, "sync.till_register_id": origRegister,
	} {
		if err := s.till.Settings.Set(context.Background(), k, v); err != nil {
			t.Fatal(err)
		}
	}
}

// seedRegisterSale seeds a local sale booked on register reg.
func seedRegisterSale(t *testing.T, s *rejoinShop, id, receipt, at, reg string) {
	t.Helper()
	seedLocalSale(t, s.till, id, receipt, at)
	if _, err := s.till.Db.Exec(`UPDATE sales SET register_id = ? WHERE id = ?`, reg, id); err != nil {
		t.Fatal(err)
	}
}

// seedRestoreJournal: before the restore (original's own unsent entries) and
// after it (made by the copy), one of each journal kind.
func seedRestoreJournal(t *testing.T, s *rejoinShop) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.till.Db.Exec(`INSERT INTO registers (id, name, is_active) VALUES (?, 'Original', 1)`, origRegister); err != nil {
		t.Fatal(err)
	}
	seedRegisterSale(t, s, "sale-pre", "T2-000000004", "2026-10-28T10:00:00Z", origRegister)
	seedRegisterSale(t, s, "sale-post", "R1a2b-000000001", "2026-10-29T10:00:00Z", origRegister)
	repo := data.NewPOSRepo(s.till.Db)
	for _, e := range []data.NoSaleEvent{
		{ID: "ns-pre", CreatedAt: "2026-10-28T11:00:00Z", RegisterID: origRegister, ActorID: "u1", Reason: "change"},
		{ID: "ns-post", CreatedAt: "2026-10-29T11:00:00Z", RegisterID: origRegister, ActorID: "u1", Reason: "change"},
	} {
		if _, err := repo.InsertNoSaleEvent(ctx, nil, e); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range []data.ClearedBasket{
		{ID: "cb-pre", CreatedAt: "2026-10-28T12:00:00Z", RegisterID: origRegister, ActorID: "u1", LineCount: 1, Total: money.FromMinor(100)},
		{ID: "cb-post", CreatedAt: "2026-10-29T12:00:00Z", RegisterID: origRegister, ActorID: "u1", LineCount: 1, Total: money.FromMinor(100)},
	} {
		if _, err := repo.InsertClearedBasket(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
}

// capturedJournal is what a fake main till received, register id by entry id.
type capturedJournal struct {
	mu   sync.Mutex
	regs map[string]string
}

func (c *capturedJournal) server(t *testing.T) *httptest.Server {
	t.Helper()
	c.regs = map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		defer c.mu.Unlock()
		if r.URL.Path == "/api/sync/sales" {
			var batch []journalSale
			if err := json.Unmarshal(raw, &batch); err != nil {
				t.Errorf("sales body: %v", err)
			}
			for _, j := range batch {
				c.regs[j.Sale.ID] = j.Sale.RegisterID
			}
		} else {
			var batch []struct{ ID, RegisterID string }
			var aux []map[string]any
			_ = json.Unmarshal(raw, &aux)
			for _, m := range aux {
				id, _ := m["id"].(string)
				reg, _ := m["register_id"].(string)
				batch = append(batch, struct{ ID, RegisterID string }{id, reg})
			}
			for _, e := range batch {
				c.regs[e.ID] = e.RegisterID
			}
		}
		_, _ = w.Write([]byte(`{"data":{"applied":1},"error":null}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestReplayUnsentJournal_RestoredCopyUsesNewRegisterAfterTheRestore(t *testing.T) {
	s := newRejoinShop(t)
	markRestoredCopy(t, s)
	seedRestoreJournal(t, s)
	var got capturedJournal
	srv := got.server(t)

	if _, err := replayUnsentJournal(context.Background(), s.till, srv.Client(), srv.URL, "b", "", newRegister); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"sale-pre": origRegister, "ns-pre": origRegister, "cb-pre": origRegister,
		"sale-post": newRegister, "ns-post": newRegister, "cb-post": newRegister,
	}
	for id, reg := range want {
		if got.regs[id] != reg {
			t.Errorf("%s sent with register %q, want %q", id, got.regs[id], reg)
		}
	}
}

// An older main till provisions no register: the post-restore entries are sent
// with none, so the main till falls back to its Main location rather than
// keeping the original's.
func TestReplayUnsentJournal_RestoredCopyWithoutNewRegisterSendsNone(t *testing.T) {
	s := newRejoinShop(t)
	markRestoredCopy(t, s)
	seedRestoreJournal(t, s)
	var got capturedJournal
	srv := got.server(t)

	if _, err := replayUnsentJournal(context.Background(), s.till, srv.Client(), srv.URL, "b", "", ""); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"sale-post", "ns-post", "cb-post"} {
		if got.regs[id] != "" {
			t.Errorf("%s sent with register %q, want none", id, got.regs[id])
		}
	}
	for _, id := range []string{"sale-pre", "ns-pre", "cb-pre"} {
		if got.regs[id] != origRegister {
			t.Errorf("%s sent with register %q, want the original's", id, got.regs[id])
		}
	}
}

// A till that is not a restored copy sends every entry unchanged.
func TestReplayUnsentJournal_NotARestoredCopySendsRegistersUnchanged(t *testing.T) {
	s := newRejoinShop(t)
	seedRestoreJournal(t, s)
	var got capturedJournal
	srv := got.server(t)

	if _, err := replayUnsentJournal(context.Background(), s.till, srv.Client(), srv.URL, "b", "", newRegister); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"sale-pre", "sale-post", "ns-pre", "ns-post", "cb-pre", "cb-post"} {
		if got.regs[id] != origRegister {
			t.Errorf("%s sent with register %q, want %q unchanged", id, got.regs[id], origRegister)
		}
	}
}

// An unreadable restore instant leaves the time-keyed entries (no-sale opens,
// cleared baskets) unchanged; the R-prefixed sale is still the copy's own.
func TestReplayUnsentJournal_UnparseableRestoreInstantLeavesTimedEntries(t *testing.T) {
	s := newRejoinShop(t)
	markRestoredCopy(t, s)
	if err := s.till.Settings.Set(context.Background(), "sync.restored_since", "garbage"); err != nil {
		t.Fatal(err)
	}
	seedRestoreJournal(t, s)
	var got capturedJournal
	srv := got.server(t)

	if _, err := replayUnsentJournal(context.Background(), s.till, srv.Client(), srv.URL, "b", "", newRegister); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ns-pre", "ns-post", "cb-pre", "cb-post", "sale-pre"} {
		if got.regs[id] != origRegister {
			t.Errorf("%s sent with register %q, want %q unchanged", id, got.regs[id], origRegister)
		}
	}
	if got.regs["sale-post"] != newRegister {
		t.Errorf("sale-post register %q, want %q", got.regs["sale-post"], newRegister)
	}
}

// End to end through the real join route: the new main till holds the copy's
// post-restore no-sale open under the register it provisioned for the copy,
// the pre-restore one under the original's, and the staged identity names the
// same register.
func TestRejoin_RestoredCopyEntriesLandOnTheNewRegister(t *testing.T) {
	s := newRejoinShop(t)
	markRestoredCopy(t, s)
	seedRestoreJournal(t, s)

	if rec := s.join(t); rec.Code != http.StatusOK {
		t.Fatalf("restored copy's re-join: %d %s", rec.Code, rec.Body.String())
	}
	newReg := stagedIdentity(t, s.tillPath).RegisterID
	if newReg == "" || newReg == origRegister {
		t.Fatalf("staged register id = %q, want the one the main till provisioned", newReg)
	}
	reg := func(table, id string) string {
		var v string
		if err := s.main.Db.QueryRow(`SELECT COALESCE(register_id, '') FROM `+table+` WHERE id = ?`, id).Scan(&v); err != nil {
			t.Fatalf("%s %s on the main till: %v", table, id, err)
		}
		return v
	}
	if got := reg("no_sale_events", "ns-post"); got != newReg {
		t.Errorf("post-restore no-sale register = %q, want %q", got, newReg)
	}
	if got := reg("cleared_baskets", "cb-post"); got != newReg {
		t.Errorf("post-restore cleared-basket register = %q, want %q", got, newReg)
	}
	if got := reg("no_sale_events", "ns-pre"); got != origRegister {
		t.Errorf("pre-restore no-sale register = %q, want the original's", got)
	}
	if got := receiptsOn(t, s.main.Db, "sale-pre", "sale-post"); len(got) != 2 {
		t.Errorf("main till sales = %v, want both", got)
	}
}
