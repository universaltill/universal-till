package data

// ut-docs#4337 (ADR-0159 §1/§4/§5): table guest tokens, the guest_order_id
// idempotency column on kiosk_counter_orders, the guest_order result
// store the cloud-result retry reads, and the held-sale payload
// compare-and-swap the directive path's async kitchen ticket uses.

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"testing"
	"time"
)

var guestTokenShape = regexp.MustCompile(`^[0-9a-f]{32}$`)

func TestCreateTable_MintsGuestToken(t *testing.T) {
	dbo, repo := openTablesTestDB(t)
	ctx := context.Background()
	id, err := repo.CreateTable(ctx, "T1", "", 2, "rect", 100, 100)
	if err != nil {
		t.Fatal(err)
	}
	var tok sql.NullString
	if err := dbo.DB.QueryRow(`SELECT guest_token FROM tables WHERE id = ?`, id).Scan(&tok); err != nil {
		t.Fatal(err)
	}
	if !guestTokenShape.MatchString(tok.String) {
		t.Fatalf("guest_token %q: want 32 lowercase hex chars", tok.String)
	}
	id2, _ := repo.CreateTable(ctx, "T2", "", 2, "rect", 100, 100)
	var tok2 string
	_ = dbo.DB.QueryRow(`SELECT guest_token FROM tables WHERE id = ?`, id2).Scan(&tok2)
	if tok2 == tok.String {
		t.Fatal("two tables share a guest token")
	}
}

func TestGuestTables_EnabledOnlyMintsMissingAndCaps(t *testing.T) {
	dbo, repo := openTablesTestDB(t)
	ctx := context.Background()
	a, _ := repo.CreateTable(ctx, "A1", "", 2, "rect", 100, 100)
	b, _ := repo.CreateTable(ctx, "B1", "", 2, "rect", 100, 100)
	c, _ := repo.CreateTable(ctx, "C1", "", 2, "rect", 100, 100)
	if err := repo.SetTableEnabled(ctx, b, false); err != nil {
		t.Fatal(err)
	}
	// A table created before migration 076 has no token yet.
	if _, err := dbo.DB.Exec(`UPDATE tables SET guest_token = NULL WHERE id = ?`, c); err != nil {
		t.Fatal(err)
	}

	got, err := repo.GuestTables(ctx, 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Label != "A1" || got[1].Label != "C1" {
		t.Fatalf("GuestTables = %+v, want A1 and C1 (enabled only)", got)
	}
	for _, g := range got {
		if !guestTokenShape.MatchString(g.Token) {
			t.Fatalf("token %q for %s", g.Token, g.Label)
		}
	}
	var stored string
	_ = dbo.DB.QueryRow(`SELECT guest_token FROM tables WHERE id = ?`, c).Scan(&stored)
	if stored != got[1].Token {
		t.Fatalf("minted token not persisted: stored %q, reported %q", stored, got[1].Token)
	}
	again, _ := repo.GuestTables(ctx, 500)
	if again[1].Token != got[1].Token {
		t.Fatal("a second report re-minted an existing token")
	}
	if capped, _ := repo.GuestTables(ctx, 1); len(capped) != 1 || capped[0].Label != "A1" {
		t.Fatalf("limit 1: %+v", capped)
	}

	tb, ok, err := repo.TableByGuestToken(ctx, got[0].Token)
	if err != nil || !ok || tb.ID != a || !tb.Enabled {
		t.Fatalf("TableByGuestToken(A1) = %+v %v %v", tb, ok, err)
	}
	var bTok string
	_ = dbo.DB.QueryRow(`SELECT guest_token FROM tables WHERE id = ?`, b).Scan(&bTok)
	if tb, ok, _ := repo.TableByGuestToken(ctx, bTok); !ok || tb.Enabled {
		t.Fatalf("disabled table: %+v %v, want found and not enabled", tb, ok)
	}
	if _, ok, _ := repo.TableByGuestToken(ctx, "nope"); ok {
		t.Fatal("unknown token found")
	}
	if _, ok, _ := repo.TableByGuestToken(ctx, ""); ok {
		t.Fatal("empty token found")
	}
}

func TestKioskCounterOrders_GuestOrderIDUniqueAndFindable(t *testing.T) {
	dbo := openMigratedDB(t, "guest_co.db")
	repo := NewKioskCounterOrdersRepo(dbo.DB)
	ctx := context.Background()

	o, err := repo.Create(ctx, KioskCounterOrder{ID: "hold-1", Status: KioskCounterOrderStatusHeld, GuestOrderID: "g1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Create(ctx, KioskCounterOrder{ID: "hold-2", Status: KioskCounterOrderStatusHeld, GuestOrderID: "g1"}); err == nil {
		t.Fatal("a second counter order with the same guest_order_id must fail")
	}
	// A LAN order has no guest id; many of them coexist.
	for i := 0; i < 2; i++ {
		if _, err := repo.Create(ctx, KioskCounterOrder{Status: KioskCounterOrderStatusHeld}); err != nil {
			t.Fatal(err)
		}
	}
	got, ok, err := repo.FindByGuestOrderID(ctx, "g1")
	if err != nil || !ok || got.ID != "hold-1" || got.DisplayNo != o.DisplayNo || got.GuestOrderID != "g1" {
		t.Fatalf("FindByGuestOrderID = %+v %v %v", got, ok, err)
	}
	if _, ok, _ := repo.FindByGuestOrderID(ctx, "other"); ok {
		t.Fatal("unknown guest order found")
	}
	if _, ok, _ := repo.FindByGuestOrderID(ctx, ""); ok {
		t.Fatal("empty guest order id must never match a LAN order")
	}
}

func TestGuestOrderResults_SaveGetUnpostedMarkPrune(t *testing.T) {
	dbo := openMigratedDB(t, "guest_results.db")
	repo := NewGuestOrderResultsRepo(dbo.DB)
	ctx := context.Background()

	r := GuestOrderResult{GuestOrderID: "g1", DirectiveID: "d1", Status: "applied", DisplayNo: "C-1", TotalMinor: 420, Currency: "EUR"}
	if err := repo.Save(ctx, r); err != nil {
		t.Fatal(err)
	}
	// Save is first-write-wins: a re-delivery never replaces the result.
	if err := repo.Save(ctx, GuestOrderResult{GuestOrderID: "g1", DirectiveID: "d1", Status: "failed", Message: "bad_order"}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := repo.Get(ctx, "g1")
	if err != nil || !ok || got.Status != "applied" || got.DisplayNo != "C-1" || got.TotalMinor != 420 || got.Currency != "EUR" || got.Posted {
		t.Fatalf("Get = %+v %v %v", got, ok, err)
	}
	_ = repo.Save(ctx, GuestOrderResult{GuestOrderID: "g2", DirectiveID: "d2", Status: "failed", Message: "unknown_table"})
	pending, err := repo.ListUnposted(ctx, 10)
	if err != nil || len(pending) != 2 {
		t.Fatalf("ListUnposted = %+v %v", pending, err)
	}
	if err := repo.MarkPosted(ctx, "g1"); err != nil {
		t.Fatal(err)
	}
	pending, _ = repo.ListUnposted(ctx, 10)
	if len(pending) != 1 || pending[0].GuestOrderID != "g2" {
		t.Fatalf("after MarkPosted: %+v", pending)
	}
	if got, _, _ := repo.Get(ctx, "g1"); !got.Posted {
		t.Fatal("posted flag not kept")
	}

	// Prune: posted rows go after a day, unposted ones after two (the
	// cloud has deleted the order by then, so a retry could only 404).
	old := time.Now().UTC().Add(-25 * time.Hour).Format(time.RFC3339)
	older := time.Now().UTC().Add(-49 * time.Hour).Format(time.RFC3339)
	_, _ = dbo.DB.Exec(`UPDATE guest_order_results SET created_at = ? WHERE guest_order_id IN ('g1','g2')`, old)
	_ = repo.Save(ctx, GuestOrderResult{GuestOrderID: "g3", DirectiveID: "d3", Status: "failed", Message: "bad_order"})
	_, _ = dbo.DB.Exec(`UPDATE guest_order_results SET created_at = ? WHERE guest_order_id = 'g3'`, older)
	if err := repo.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := repo.Get(ctx, "g1"); ok {
		t.Fatal("posted row older than a day survived the prune")
	}
	if _, ok, _ := repo.Get(ctx, "g2"); !ok {
		t.Fatal("unposted row younger than two days was pruned")
	}
	if _, ok, _ := repo.Get(ctx, "g3"); ok {
		t.Fatal("unposted row older than two days survived the prune")
	}
}

func TestGuestOrderResults_ListUnpostedIsBounded(t *testing.T) {
	dbo := openMigratedDB(t, "guest_results_cap.db")
	repo := NewGuestOrderResultsRepo(dbo.DB)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_ = repo.Save(ctx, GuestOrderResult{GuestOrderID: fmt.Sprintf("g%d", i), DirectiveID: "d", Status: "failed", Message: "bad_order"})
	}
	if got, _ := repo.ListUnposted(ctx, 3); len(got) != 3 {
		t.Fatalf("ListUnposted(3) returned %d", len(got))
	}
}

func TestHeldSales_SwapPayload(t *testing.T) {
	dbo := openMigratedDB(t, "held_swap.db")
	repo := NewHeldSalesRepo(dbo.DB)
	ctx := context.Background()
	if err := repo.Upsert(ctx, HeldSale{ID: "hold-x", Label: "C-1", Payload: `{"v":1}`}); err != nil {
		t.Fatal(err)
	}
	ok, err := repo.SwapPayload(ctx, "hold-x", `{"v":0}`, `{"v":2}`)
	if err != nil || ok {
		t.Fatalf("stale old payload: swapped=%v err=%v, want false", ok, err)
	}
	ok, err = repo.SwapPayload(ctx, "hold-x", `{"v":1}`, `{"v":2}`)
	if err != nil || !ok {
		t.Fatalf("matching payload: swapped=%v err=%v, want true", ok, err)
	}
	h, _, _ := repo.Get(ctx, "hold-x")
	if h.Payload != `{"v":2}` {
		t.Fatalf("payload = %s", h.Payload)
	}
	if ok, _ := repo.SwapPayload(ctx, "gone", `{"v":2}`, `{"v":3}`); ok {
		t.Fatal("a missing row must never be swapped (or recreated)")
	}
}
