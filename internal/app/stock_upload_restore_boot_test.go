package app

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#4763: a restored backup restarts the stock-upload stream and owes
// every stock level again.
func TestRotateStockStreamAfterRestore_NewStreamAndEverythingOwed(t *testing.T) {
	sqlDB := openBackfillBootDB(t) // a migrated db
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('r1', 'legacy-1', 'loc_main', 3)`,
		`INSERT INTO inventory (id, item_id, location_id, quantity) VALUES ('r2', 'legacy-2', 'loc_main', 4)`,
		`DELETE FROM stock_upload_outbox`, // as if the backup's marks were all answered
	} {
		if _, err := sqlDB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	repo := data.NewStockUploadRepo(sqlDB)
	before, _ := repo.Stream(ctx)
	log := &recordingLog{}
	rotateStockStreamAfterRestore(ctx, sqlDB, log)
	after, err := repo.Stream(ctx)
	if err != nil || after.ID == before.ID || !after.StartedAt.After(before.StartedAt) {
		t.Fatalf("stream %+v -> %+v, %v", before, after, err)
	}
	var marks int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM stock_upload_outbox`).Scan(&marks); err != nil || marks != 2 {
		t.Fatalf("marks = %d, %v; want every inventory row owed again", marks, err)
	}
	if len(log.errors) != 0 || len(log.infos) != 1 {
		t.Fatalf("log = %+v", log)
	}
}

// The call site is part of the contract: app.Run applies the restore before
// Open, so the rotation must follow the open and be conditioned on it.
func TestRunRotatesTheStockStreamWhenARestoreWasApplied(t *testing.T) {
	raw, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	i := strings.Index(src, "restoreApplied = true")
	j := strings.Index(src, "if restoreApplied {")
	k := strings.Index(src, "rotateStockStreamAfterRestore(ctx, database.DB, log)")
	if i < 0 || j < i || k < j {
		t.Fatal("app.Run must set restoreApplied when ApplyPendingRestore applied, then call rotateStockStreamAfterRestore under it after db.Open")
	}
}
