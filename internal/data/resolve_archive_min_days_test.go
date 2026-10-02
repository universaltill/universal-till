package data_test

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#3365: ResolveArchiveMinDays is the exported form of the lookup
// DeleteResetBatch gates on: the shop country's archive_min_days, or the
// global floor when no country is chosen.
func TestResolveArchiveMinDays(t *testing.T) {
	d, x, _ := resetTestDB(t, "resolve_archive_min_days.db")
	ctx := context.Background()

	got, err := data.ResolveArchiveMinDays(ctx, d.DB)
	if err != nil || got != data.GlobalArchiveMinDays {
		t.Fatalf("no country: got %d, %v; want %d, nil", got, err, data.GlobalArchiveMinDays)
	}

	x(`UPDATE country_settings SET archive_min_days = 4000 WHERE code = 'GB'`)
	x(`INSERT INTO settings (key, value) VALUES ('store.country', 'GB')`)
	got, err = data.ResolveArchiveMinDays(ctx, d.DB)
	if err != nil || got != 4000 {
		t.Fatalf("GB: got %d, %v; want 4000, nil", got, err)
	}
}
