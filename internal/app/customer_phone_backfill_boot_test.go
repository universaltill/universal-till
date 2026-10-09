package app

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

// ut-docs#3200 (ADR-0131 §3): every till — main and replica — back-fills
// customers.phone_e164 in the background after start-up, never fatally.

func openPhoneBootDB(t *testing.T, primaryURL string) *sql.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "unitill-pos.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx := context.Background()
	settings := data.NewSettingsRepo(d.DB)
	if err := settings.Set(ctx, data.StoreCountrySettingsKey, "GB"); err != nil {
		t.Fatal(err)
	}
	if primaryURL != "" {
		if err := settings.Set(ctx, "sync.primary_url", primaryURL); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.DB.Exec(`INSERT INTO customers (id, name, phone) VALUES ('c1', 'Alice', '020 7946 0018'), ('c2', 'Bob', NULL)`); err != nil {
		t.Fatal(err)
	}
	return d.DB
}

func pendingPhoneE164(t *testing.T, sqlDB *sql.DB) int {
	t.Helper()
	var n int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM customers WHERE phone_e164 IS NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestStartCustomerPhoneE164Backfill_RunsOnEveryTill(t *testing.T) {
	for name, primaryURL := range map[string]string{
		"main":    "",
		"replica": "http://primary.local:8080",
	} {
		t.Run(name, func(t *testing.T) {
			sqlDB := openPhoneBootDB(t, primaryURL)
			lg := &recordingLog{}
			var wg sync.WaitGroup

			startCustomerPhoneE164Backfill(context.Background(), &wg, sqlDB, lg)
			wg.Wait() // registered on wg, so shutdown waits for it

			if n := pendingPhoneE164(t, sqlDB); n != 0 {
				t.Fatalf("%d customers still without phone_e164", n)
			}
			var v string
			if err := sqlDB.QueryRow(`SELECT phone_e164 FROM customers WHERE id = 'c1'`).Scan(&v); err != nil || v != "+442079460018" {
				t.Errorf("c1 phone_e164 = %q (%v), want +442079460018", v, err)
			}
			if len(lg.errors) != 0 {
				t.Errorf("unexpected errors: %v", lg.errors)
			}
			if len(lg.infos) != 1 || !strings.Contains(lg.infos[0], "2") {
				t.Errorf("want one info line naming the count 2, got %v", lg.infos)
			}

			// Second boot: nothing to do, nothing logged.
			lg2 := &recordingLog{}
			startCustomerPhoneE164Backfill(context.Background(), &wg, sqlDB, lg2)
			wg.Wait()
			if len(lg2.infos)+len(lg2.errors) != 0 {
				t.Errorf("second boot logged %v / %v, want silence", lg2.infos, lg2.errors)
			}
		})
	}
}

func TestStartCustomerPhoneE164Backfill_ShutdownIsQuiet(t *testing.T) {
	sqlDB := openPhoneBootDB(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	lg := &recordingLog{}
	var wg sync.WaitGroup
	startCustomerPhoneE164Backfill(ctx, &wg, sqlDB, lg)
	wg.Wait()
	if len(lg.errors) != 0 {
		t.Errorf("a cancelled boot logged errors: %v", lg.errors)
	}
}

func TestStartCustomerPhoneE164Backfill_ErrorIsLoggedNotFatal(t *testing.T) {
	sqlDB := openPhoneBootDB(t, "")
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	lg := &recordingLog{}
	var wg sync.WaitGroup
	startCustomerPhoneE164Backfill(context.Background(), &wg, sqlDB, lg)
	wg.Wait()
	if len(lg.errors) != 1 || !strings.Contains(lg.errors[0], "phone_e164") {
		t.Errorf("want one logged error naming phone_e164, got %v", lg.errors)
	}
}
