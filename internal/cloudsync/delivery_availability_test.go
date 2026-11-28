package cloudsync

import (
	"context"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#6583: on every tick the main till turns an item whose "unavailable
// until" has passed back to available (local, offline-safe) and queues the
// neutral signal; a satellite never does.
func TestExpireDeliveryAvailability_MainTillOnly(t *testing.T) {
	dbo, err := db.Open(testsupport.MigratedDBFile(t, "avail.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dbo.Close() })
	testsupport.SeedTaxCode(t, dbo.DB, "tc1", "Standard", 2000)
	testsupport.SeedItem(t, dbo.DB, testsupport.ItemSeed{ID: "itm1", SKU: "A-1", Name: "Soup", BasePrice: 500, TaxCodeID: "tc1", IsActive: true})
	repo := data.NewDeliveryAvailabilityRepo(dbo.DB)
	now := time.Now().UTC()
	if err := repo.SetItemUnavailable(context.Background(), "itm1", now.Add(time.Hour), "", now); err != nil {
		t.Fatal(err)
	}
	// A satellite leaves it alone.
	if _, err := dbo.DB.Exec(`INSERT INTO settings(key,value) VALUES('sync.primary_url','http://main.local')`); err != nil {
		t.Fatal(err)
	}
	if n := expireDeliveryAvailability(context.Background(), dbo.DB, now.Add(2*time.Hour)); n != 0 {
		t.Fatalf("satellite cleared %d", n)
	}
	if _, err := dbo.DB.Exec(`DELETE FROM settings WHERE key='sync.primary_url'`); err != nil {
		t.Fatal(err)
	}
	if n := expireDeliveryAvailability(context.Background(), dbo.DB, now.Add(30*time.Minute)); n != 0 {
		t.Fatalf("cleared early: %d", n)
	}
	if n := expireDeliveryAvailability(context.Background(), dbo.DB, now.Add(2*time.Hour)); n != 1 {
		t.Fatalf("cleared %d, want 1", n)
	}
}
