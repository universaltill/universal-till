package pages

import (
	"context"
	"strings"
	"testing"
	"time"
)

// ut-docs#5428: the re-join replay halves on a 413 too.

func TestReplayUnsentJournal_413HalvesAndSendsEverySale(t *testing.T) {
	r := newPushRig(t)
	r.seed(t, 50)
	r.fake.limit.Store(int64(3 * r.oneSale))
	out, err := replayUnsentJournal(context.Background(), r.replicaDp, r.client, r.srv.URL, "token-abc", "", "")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if out.Sales != 50 {
		t.Fatalf("sales sent = %d", out.Sales)
	}
	if n := primarySaleCount(t, r.primaryDp, "p-%"); n != 50 {
		t.Fatalf("primary has %d of 50", n)
	}
}

func TestReplayUnsentJournal_SingleOversizedSaleFailsNamingReceipt(t *testing.T) {
	r := newPushRig(t)
	base := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	seedLocalSale(t, r.replicaDp, "h-a", "R-h-a", base.Format(time.RFC3339))
	r.seedBig(t, "h-b", base.Add(time.Second), 20*r.oneSale)
	seedLocalSale(t, r.replicaDp, "h-c", "R-h-c", base.Add(2*time.Second).Format(time.RFC3339))
	r.fake.limit.Store(int64(3 * r.oneSale))
	before := countUnsentSales(context.Background(), r.replicaDp)
	_, err := replayUnsentJournal(context.Background(), r.replicaDp, r.client, r.srv.URL, "token-abc", "", "")
	if err == nil || !strings.Contains(err.Error(), "R-h-b") {
		t.Fatalf("want error naming R-h-b, got %v", err)
	}
	if !r.onPrimary(t, "h-a") || r.onPrimary(t, "h-b") {
		t.Fatal("h-a should be sent and h-b not")
	}
	if after := countUnsentSales(context.Background(), r.replicaDp); after != before {
		t.Fatalf("unsent changed %d -> %d", before, after)
	}
}
