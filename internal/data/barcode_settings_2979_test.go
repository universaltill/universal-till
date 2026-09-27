package data

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// ut-docs#2979: an additional till computes the next barcode symbology set
// without writing it, and every generic write path of the key -- including
// the admin pull -- drops the in-process cache.

func TestNextBarcodeSymbologySet_ComputesWithoutWriting(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "next.db")
	repo := NewSettingsRepo(d.DB)
	mustExec(t, d, `INSERT INTO settings (key, value) VALUES ('`+BarcodeEnabledSymbologiesKey+`', '["EAN13","CODE128"]')`)

	ids, err := repo.NextBarcodeSymbologySet(ctx, "EAN13", false)
	if err != nil || !slices.Equal(ids, []string{"CODE128"}) {
		t.Fatalf("next set = %v (%v), want [CODE128]", ids, err)
	}
	if v, _, _ := repo.Get(ctx, BarcodeEnabledSymbologiesKey); v != `["EAN13","CODE128"]` {
		t.Fatalf("stored value = %q, want unchanged — Next must not write", v)
	}
	if _, err := repo.NextBarcodeSymbologySet(ctx, "CODE128", false); err != nil {
		t.Fatalf("disabling one of two: %v", err)
	}
	mustExec(t, d, `UPDATE settings SET value = '["EAN13"]' WHERE key = '`+BarcodeEnabledSymbologiesKey+`'`)
	if _, err := repo.NextBarcodeSymbologySet(ctx, "EAN13", false); !errors.Is(err, ErrEmptyBarcodeSymbologySet) {
		t.Fatalf("disabling the last one = %v, want ErrEmptyBarcodeSymbologySet", err)
	}
}

func TestNextBarcodeSymbologySet_NoRowStartsFromDefaults(t *testing.T) {
	d := openMigratedDB(t, "next-default.db")
	mustExec(t, d, `DELETE FROM settings WHERE key = '`+BarcodeEnabledSymbologiesKey+`'`)
	ids, err := NewSettingsRepo(d.DB).NextBarcodeSymbologySet(context.Background(), "EAN13_WEIGHT_PREFIX2X", true)
	if err != nil {
		t.Fatal(err)
	}
	want := append(DefaultEnabledBarcodeSymbologyIDs(), "EAN13_WEIGHT_PREFIX2X")
	if !slices.Equal(ids, want) {
		t.Fatalf("next set = %v, want %v", ids, want)
	}
}

func TestApplyAdmin_InvalidatesBarcodeSymbologyCache(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")
	repo := NewSettingsRepo(replica.DB)
	if _, err := repo.EnabledBarcodeSymbologies(ctx); err != nil { // prime the cache
		t.Fatal(err)
	}
	mustExec(t, primary, `INSERT OR REPLACE INTO settings (key, value) VALUES ('`+BarcodeEnabledSymbologiesKey+`', '["CODE39"]')`)
	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatal(err)
	}
	if ids, _ := repo.EnabledBarcodeSymbologies(ctx); !slices.Equal(ids, []string{"CODE39"}) {
		t.Fatalf("replica cached set after the pull = %v, want [CODE39]", ids)
	}
}
