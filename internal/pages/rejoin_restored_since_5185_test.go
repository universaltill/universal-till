package pages

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/paths"
)

// ut-docs#5185: the original till's clock ran ahead of the copy's, so its
// pre-restore no-sale open and cleared basket are stamped LATER than the
// copy's real "now". The restore check must place sync.restored_since after
// them, so the replay keeps them on the original's register.
func TestReplay_OriginalEntriesStampedAheadOfTheCopyKeepTheirRegister(t *testing.T) {
	orig := paths.DataDir()
	paths.Init(t.TempDir()) // no install marker: the DB's install id reads as foreign
	t.Cleanup(func() { paths.Init(orig) })

	s := newRejoinShop(t) // a joined (replica) till
	ctx := context.Background()
	if err := s.till.Settings.Set(ctx, "till_identity.install_id", "install-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.till.Db.Exec(`INSERT INTO registers (id, name, is_active) VALUES (?, 'Original', 1)`, origRegister); err != nil {
		t.Fatal(err)
	}
	if err := s.till.Settings.Set(ctx, "sync.till_register_id", origRegister); err != nil {
		t.Fatal(err)
	}

	ahead := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	repo := data.NewPOSRepo(s.till.Db)
	if _, err := repo.InsertNoSaleEvent(ctx, nil, data.NoSaleEvent{ID: "ns-ahead", CreatedAt: ahead.Format(time.RFC3339), RegisterID: origRegister, ActorID: "u1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.InsertClearedBasket(ctx, data.ClearedBasket{ID: "cb-ahead", CreatedAt: ahead.Add(-time.Minute).Format(time.RFC3339), RegisterID: origRegister, ActorID: "u1", LineCount: 1, Total: money.FromMinor(100)}); err != nil {
		t.Fatal(err)
	}

	// The reset rewrites enroll's process-wide identity: put it back after.
	t.Cleanup(func() { enroll.Init(context.Background(), &config.Config{}, newMemKV(), &sync.WaitGroup{}) })
	if !enroll.CheckRestoredInstall(ctx, &config.Config{}, s.till.Settings, repo.NewestLocalJournalEntryAt) {
		t.Fatal("want the restore detected")
	}
	// Entries the copy makes after the restore carry the (clock-skewed) copy
	// time, which is after the computed instant.
	after := ahead.Add(5 * time.Second).Format(time.RFC3339)
	if _, err := repo.InsertNoSaleEvent(ctx, nil, data.NoSaleEvent{ID: "ns-copy", CreatedAt: after, RegisterID: origRegister, ActorID: "u1"}); err != nil {
		t.Fatal(err)
	}

	var got capturedJournal
	srv := got.server(t)
	if _, err := replayUnsentJournal(ctx, s.till, srv.Client(), srv.URL, "b", "", newRegister); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"ns-ahead": origRegister, "cb-ahead": origRegister, "ns-copy": newRegister} {
		if got.regs[id] != want {
			t.Errorf("%s sent with register %q, want %q", id, got.regs[id], want)
		}
	}
}
