package data

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// ut-docs#6663 (a): the permanent receipt/invoice numbers come from a stored
// counter, so "Clear transaction history" never reuses one.

func sellN(t *testing.T, dbx *posTestDB, prefix string, n int) []string {
	t.Helper()
	var got []string
	for i := 0; i < n; i++ {
		no := mustNextReceipt(t, dbx)
		seedRestoreSale(t, dbx, no, time.Now().UTC().Add(time.Duration(i)*time.Second))
		got = append(got, no)
	}
	return got
}

func TestNextReceiptNo_ClearHistoryNeverReusesNumbers(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	got := sellN(t, dbx, "", 3)
	if got[2] != "000000003" {
		t.Fatalf("third receipt = %q", got[2])
	}
	if _, _, err := dbx.repo.ResetTransactionHistory(context.Background(), "", ""); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if next := mustNextReceipt(t, dbx); next != "000000004" {
		t.Fatalf("receipt after clear history = %q, want 000000004", next)
	}
}

func TestNextReceiptNo_ClearHistoryKeepsPrefixSeries(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	setRestoreSetting(t, dbx, "sync.receipt_prefix", "2-")
	sellN(t, dbx, "2-", 2)
	if _, _, err := dbx.repo.ResetTransactionHistory(context.Background(), "", ""); err != nil {
		t.Fatal(err)
	}
	if next := mustNextReceipt(t, dbx); next != "2-000000003" {
		t.Fatalf("got %q, want 2-000000003", next)
	}
}

func TestNextReceiptNo_SeedsFromArchiveWhenNoCounterRow(t *testing.T) {
	// A till upgraded after it already cleared its history: archive rows, no
	// number_series row. The next sale must continue above the archive.
	dbx := newPOSLifecycleTestDB(t)
	sellN(t, dbx, "", 5)
	if _, _, err := dbx.repo.ResetTransactionHistory(context.Background(), "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := dbx.d.DB.Exec(`DELETE FROM number_series`); err != nil {
		t.Fatal(err)
	}
	if next := mustNextReceipt(t, dbx); next != "000000006" {
		t.Fatalf("got %q, want 000000006", next)
	}
}

func TestNextReceiptNo_UpgradeFromOldNumbersContinues(t *testing.T) {
	for _, prefix := range []string{"", "T2-", "M3f2a9c1e-", "R1a2b-"} {
		t.Run("prefix="+prefix, func(t *testing.T) {
			dbx := newPOSLifecycleTestDB(t)
			if prefix != "" {
				setRestoreSetting(t, dbx, "sync.receipt_prefix", prefix)
			}
			seedRestoreSale(t, dbx, prefix+"000000123", time.Now().UTC())
			if got := mustNextReceipt(t, dbx); got != prefix+"000000124" {
				t.Fatalf("got %q, want %s000000124", got, prefix)
			}
		})
	}
}

func TestNextReceiptNo_EmptyPrefixIgnoresPrefixedRows(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	seedRestoreSale(t, dbx, "T2-000000900", time.Now().UTC())
	seedRestoreSale(t, dbx, "000000007", time.Now().UTC())
	if got := mustNextReceipt(t, dbx); got != "000000008" {
		t.Fatalf("got %q, want 000000008", got)
	}
}

func TestNextReceiptNo_CounterPersistsAcrossRepoRestart(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	mustNextReceipt(t, dbx) // 1, no sale row written: the counter alone remembers
	mustNextReceipt(t, dbx) // 2
	fresh := NewPOSRepo(dbx.d.DB)
	got, err := fresh.NextReceiptNo(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "000000003" {
		t.Fatalf("after restart got %q, want 000000003", got)
	}
}

func TestNextReceiptNo_RestoreFloorStillAppliesWithCounterRow(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	mustNextReceipt(t, dbx)
	setRestoreSetting(t, dbx, RestoreReceiptFloorPrefixKey, "")
	setRestoreSetting(t, dbx, RestoreReceiptFloorKey, "11000")
	if got := mustNextReceipt(t, dbx); got != "000011001" {
		t.Fatalf("got %q, want 000011001", got)
	}
	if got := mustNextReceipt(t, dbx); got != "000011002" {
		t.Fatalf("got %q, want 000011002", got)
	}
}

func TestReseedReceiptCounter_HealsCounterBehindSales(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	mustNextReceipt(t, dbx)
	seedRestoreSale(t, dbx, "000000050", time.Now().UTC())
	if err := dbx.repo.ReseedReceiptCounter(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := mustNextReceipt(t, dbx); got != "000000051" {
		t.Fatalf("got %q, want 000000051", got)
	}
}

func TestNumberSeriesNotInResetTableList(t *testing.T) {
	for _, tb := range resetArchiveTables {
		if tb.live == "number_series" {
			t.Fatal("number_series must survive Clear transaction history")
		}
	}
}

func seedInvoiceFor(t *testing.T, dbx *posTestDB, repo *InvoiceRepo, series, saleID string) InvoiceRow {
	t.Helper()
	seedRestoreSale(t, dbx, saleID, time.Now().UTC())
	row, err := repo.Create(context.Background(), InvoiceInput{
		Series: series, Kind: "invoice", SaleID: "s-" + saleID, SellerJSON: "{}",
		VATBreakdownJSON: "[]", IssuedAt: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("create invoice: %v", err)
	}
	return row
}

func TestInvoiceCreate_ClearHistoryNeverReusesNumbers(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	inv := NewInvoiceRepo(dbx.d.DB)
	for i := 1; i <= 3; i++ {
		if r := seedInvoiceFor(t, dbx, inv, "", fmt.Sprintf("s%d", i)); r.InvoiceNo != int64(i) {
			t.Fatalf("invoice %d got no %d", i, r.InvoiceNo)
		}
	}
	if _, _, err := dbx.repo.ResetTransactionHistory(context.Background(), "", ""); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if r := seedInvoiceFor(t, dbx, inv, "", "s9"); r.InvoiceNo != 4 {
		t.Fatalf("invoice after clear = %d, want 4", r.InvoiceNo)
	}
}

func TestInvoiceCreate_SeedsFromArchiveWithoutCounterRow(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	inv := NewInvoiceRepo(dbx.d.DB)
	seedInvoiceFor(t, dbx, inv, "2-", "a1")
	seedInvoiceFor(t, dbx, inv, "2-", "a2")
	if _, _, err := dbx.repo.ResetTransactionHistory(context.Background(), "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := dbx.d.DB.Exec(`DELETE FROM number_series`); err != nil {
		t.Fatal(err)
	}
	if r := seedInvoiceFor(t, dbx, inv, "2-", "a3"); r.InvoiceNo != 3 {
		t.Fatalf("got %d, want 3", r.InvoiceNo)
	}
}

func TestSetFirstReceiptPrefix(t *testing.T) {
	ctx := context.Background()
	t.Run("sets once on a till that has not traded", func(t *testing.T) {
		dbx := newPOSLifecycleTestDB(t)
		if ok, err := dbx.repo.SetFirstReceiptPrefix(ctx, "1-"); err != nil || !ok {
			t.Fatalf("set = %v, %v", ok, err)
		}
		if ok, _ := dbx.repo.SetFirstReceiptPrefix(ctx, "A-"); ok {
			t.Fatal("a till that already holds a prefix must not be changed")
		}
		if got := mustNextReceipt(t, dbx); got != "1-000000001" {
			t.Fatalf("first receipt %q", got)
		}
	})
	t.Run("never touches a till that has traded", func(t *testing.T) {
		dbx := newPOSLifecycleTestDB(t)
		seedRestoreSale(t, dbx, "000000009", time.Now().UTC())
		if ok, _ := dbx.repo.SetFirstReceiptPrefix(ctx, "1-"); ok {
			t.Fatal("prefix set on a till with sales")
		}
	})
	t.Run("never touches a till with archived sales", func(t *testing.T) {
		dbx := newPOSLifecycleTestDB(t)
		sellN(t, dbx, "", 1)
		if _, _, err := dbx.repo.ResetTransactionHistory(ctx, "", ""); err != nil {
			t.Fatal(err)
		}
		if ok, _ := dbx.repo.SetFirstReceiptPrefix(ctx, "1-"); ok {
			t.Fatal("prefix set on a till with archived sales")
		}
	})
}

func TestListSalesJournal_SearchByEitherNumber(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	now := time.Now().UTC().Format(time.RFC3339)
	for id, nos := range map[string][2]string{"a": {"1-000000007", "42"}, "b": {"2-000000008", "142"}, "c": {"000000042", ""}} {
		if _, err := dbx.d.DB.Exec(`INSERT INTO sales (id, receipt_no, display_no, subtotal, total, created_at) VALUES (?, ?, ?, 1, 1, ?)`,
			id, nos[0], nos[1], now); err != nil {
			t.Fatal(err)
		}
	}
	find := func(q string) string {
		rows, _, err := dbx.repo.ListSalesJournal(context.Background(), SalesJournalFilter{AllTills: true, Limit: 10, Search: q})
		if err != nil {
			t.Fatal(err)
		}
		var out string
		for _, r := range rows {
			out += r.ReceiptNo + "|" + r.DisplayNo + ";"
		}
		return out
	}
	if got := find("42"); got != "000000042|;1-000000007|42;" && got != "1-000000007|42;000000042|;" {
		t.Fatalf("search 42 (order number of a, tail of c's receipt) = %q", got)
	}
	if got := find("1-000000007"); got != "1-000000007|42;" {
		t.Fatalf("exact receipt = %q", got)
	}
	if got := find("142"); got != "2-000000008|142;" {
		t.Fatalf("order number 142 = %q", got)
	}
	if got := find("%"); got != "" {
		t.Fatalf("a literal %% must not match everything: %q", got)
	}
}

// A restore jump must start above the stored counter and the archive, not
// only above the live sales: a till that cleared its history has live sales
// far below the numbers it already issued (review F3).
func TestJumpNumbersAfterRestore_FloorIsAboveCounterAndArchive(t *testing.T) {
	ctx := context.Background()
	dbx := newPOSLifecycleTestDB(t)
	seedRestoreSale(t, dbx, "000050000", time.Now().UTC().Add(-time.Hour))
	if got := mustNextReceipt(t, dbx); got != "000050001" { // counter row = 50001
		t.Fatalf("got %q", got)
	}
	if _, _, err := dbx.repo.ResetTransactionHistory(ctx, "", ""); err != nil {
		t.Fatal(err)
	}
	jump, err := dbx.repo.JumpNumbersAfterRestore(ctx, time.Now().UTC(), "tok-1")
	if err != nil {
		t.Fatal(err)
	}
	if jump.ReceiptFloor < 50001+restoreMinJump {
		t.Fatalf("receipt floor %d, want at least %d (counter 50001 + jump)", jump.ReceiptFloor, 50001+restoreMinJump)
	}
	if got := mustNextReceipt(t, dbx); got != fmt.Sprintf("%09d", jump.ReceiptFloor+1) {
		t.Fatalf("next receipt %q, want floor+1 = %d", got, jump.ReceiptFloor+1)
	}
}
