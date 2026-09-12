package data

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

// findAdminTable returns the adminTables entry for name, failing the test if
// it's ever renamed/removed — these tests intentionally exercise the real
// table shape rather than a fabricated one.
func findAdminTable(t *testing.T, name string) adminTable {
	t.Helper()
	for _, at := range adminTables {
		if at.name == name {
			return at
		}
	}
	t.Fatalf("adminTables has no entry named %q", name)
	return adminTable{}
}

// TestBuildUpsertBatches_GroupsBySignatureAndChunks is ut-docs#1369's core
// regression test: it exercises buildUpsertBatches directly (no DB), so it
// pins the grouping/chunking behaviour itself rather than just the
// end-to-end applied result, which the old one-exec-per-row code would also
// have passed.
func TestBuildUpsertBatches_GroupsBySignatureAndChunks(t *testing.T) {
	taxCodes := findAdminTable(t, "tax_codes")
	cols := []string{"id", "name", "rate_basis_points", "is_active", "takeaway_rate_basis_points"}

	// 250 identically-shaped rows should collapse into ceil(250/chunkRows)
	// batches, each holding at most chunkRows rows, none holding zero.
	recs := make([]map[string]any, 250)
	for i := range recs {
		recs[i] = map[string]any{
			"id": fmt.Sprintf("tc%d", i), "name": fmt.Sprintf("Tax %d", i),
			"rate_basis_points": int64(2000), "is_active": int64(1),
			"takeaway_rate_basis_points": int64(0),
		}
	}
	batches := buildUpsertBatches(taxCodes, cols, recs)

	wantChunkRows := maxBatchPlaceholders / len(cols)
	wantBatches := (len(recs) + wantChunkRows - 1) / wantChunkRows
	if len(batches) != wantBatches {
		t.Fatalf("got %d batches, want %d (chunkRows=%d for %d cols)", len(batches), wantBatches, wantChunkRows, len(cols))
	}
	total := 0
	for i, b := range batches {
		if len(b.rows) == 0 {
			t.Fatalf("batch %d is empty", i)
		}
		if len(b.rows) > wantChunkRows {
			t.Fatalf("batch %d holds %d rows, exceeds chunk bound %d", i, len(b.rows), wantChunkRows)
		}
		total += len(b.rows)
	}
	if total != len(recs) {
		t.Fatalf("batches carry %d rows total, want %d — a row was dropped or duplicated", total, len(recs))
	}

	// Every row must actually be present, in the resolved column order —
	// find id "tc137" (an arbitrary middle row) and check its values landed
	// in some batch, untouched.
	found := false
	for _, b := range batches {
		idIdx, rateIdx := -1, -1
		for i, n := range b.names {
			switch n {
			case "id":
				idIdx = i
			case "rate_basis_points":
				rateIdx = i
			}
		}
		for _, row := range b.rows {
			if row[idIdx] == "tc137" {
				found = true
				if row[rateIdx] != int64(2000) {
					t.Fatalf("tc137 rate_basis_points = %v, want 2000", row[rateIdx])
				}
			}
		}
	}
	if !found {
		t.Fatal("row tc137 missing from any batch")
	}
}

// TestBuildUpsertBatches_DifferingColumnSetsGetSeparateGroups proves rows
// that resolve to a different column signature (e.g. a rolling-upgrade
// primary whose bundle is missing a newer column for some rows) never share
// a batch — each keeps resolveUpsertRow's original per-row semantics, just grouped
// with rows shaped identically to itself.
func TestBuildUpsertBatches_DifferingColumnSetsGetSeparateGroups(t *testing.T) {
	taxCodes := findAdminTable(t, "tax_codes")
	cols := []string{"id", "name", "rate_basis_points", "is_active", "takeaway_rate_basis_points"}

	recs := []map[string]any{
		{"id": "full1", "name": "Full One", "rate_basis_points": int64(2000), "is_active": int64(1), "takeaway_rate_basis_points": int64(0)},
		{"id": "full2", "name": "Full Two", "rate_basis_points": int64(500), "is_active": int64(1), "takeaway_rate_basis_points": int64(0)},
		// Missing takeaway_rate_basis_points entirely — an older primary's
		// row shape (resolveUpsertRow's "column the primary doesn't know" case).
		{"id": "narrow1", "name": "Narrow One", "rate_basis_points": int64(1000), "is_active": int64(1)},
	}
	batches := buildUpsertBatches(taxCodes, cols, recs)
	if len(batches) != 2 {
		t.Fatalf("got %d batches, want 2 (one per distinct column signature), batches=%+v", len(batches), batches)
	}
	rowCounts := map[int]int{}
	for _, b := range batches {
		rowCounts[len(b.names)] += len(b.rows)
	}
	if rowCounts[len(cols)] != 2 {
		t.Fatalf("expected 2 full-shape rows grouped together, got %d", rowCounts[len(cols)])
	}
	if rowCounts[len(cols)-1] != 1 {
		t.Fatalf("expected 1 narrow-shape row in its own group, got %d", rowCounts[len(cols)-1])
	}
}

// TestAdminApplyLargeTableBatchesAcrossMultipleChunks is the real-DB
// end-to-end companion: hundreds of tax_codes rows, applied through the
// actual ApplyAdmin path, forcing buildUpsertBatches to split them across
// multiple chunked INSERT statements (chunkRows = maxBatchPlaceholders/5 =
// 100, so 250 rows spans 3 statements). Verifies every row lands intact,
// AND that a second apply (rows now already present) correctly hits the
// ON CONFLICT UPDATE branch for a batched statement, not just a single-row
// one — an update touching every row in a 250-row batch, not just the
// tail/head of a chunk.
func TestAdminApplyLargeTableBatchesAcrossMultipleChunks(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")

	const n = 250
	for i := 0; i < n; i++ {
		mustExec(t, primary,
			`INSERT INTO tax_codes (id, name, rate_basis_points, is_active) VALUES (?, ?, ?, 1)`,
			fmt.Sprintf("tc%03d", i), fmt.Sprintf("Tax %03d", i), int64(1000+i))
	}

	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("first apply: %v", err)
	}

	// +3: migration 001 seeds tax_std/tax_red/tax_zero on every fresh DB —
	// both sides start with them, so ApplyAdmin's own upsert of those three
	// (identical content) doesn't change the count, but they're still real
	// rows in the table alongside the n new ones this test adds.
	const seeded = 3
	var count int
	if err := replica.QueryRow(`SELECT COUNT(*) FROM tax_codes`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != n+seeded {
		t.Fatalf("replica has %d tax_codes rows after first apply, want %d (%d new + %d seeded)", count, n+seeded, n, seeded)
	}
	for _, probe := range []int{0, 1, 99, 100, 101, 249} {
		var rate int64
		id := fmt.Sprintf("tc%03d", probe)
		if err := replica.QueryRow(`SELECT rate_basis_points FROM tax_codes WHERE id = ?`, id).Scan(&rate); err != nil {
			t.Fatalf("row %s missing after first apply: %v", id, err)
		}
		if want := int64(1000 + probe); rate != want {
			t.Fatalf("row %s rate = %d, want %d", id, rate, want)
		}
	}

	// Mutate every row on the primary (every row's rate shifts by +1), then
	// re-dump/re-apply: this forces the ON CONFLICT UPDATE branch across an
	// entire multi-hundred-row, multi-chunk batch.
	mustExec(t, primary, `UPDATE tax_codes SET rate_basis_points = rate_basis_points + 1`)
	bundle2, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("re-dump: %v", err)
	}
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle2)); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if err := replica.QueryRow(`SELECT COUNT(*) FROM tax_codes`).Scan(&count); err != nil {
		t.Fatalf("count after second apply: %v", err)
	}
	if count != n+seeded {
		t.Fatalf("replica has %d tax_codes rows after second apply, want %d (no duplicate/dropped rows)", count, n+seeded)
	}
	for _, probe := range []int{0, 99, 100, 249} {
		var rate int64
		id := fmt.Sprintf("tc%03d", probe)
		if err := replica.QueryRow(`SELECT rate_basis_points FROM tax_codes WHERE id = ?`, id).Scan(&rate); err != nil {
			t.Fatalf("row %s missing after second apply: %v", id, err)
		}
		if want := int64(1000+probe) + 1; rate != want {
			t.Fatalf("row %s rate after update = %d, want %d (batched ON CONFLICT UPDATE didn't apply)", id, rate, want)
		}
	}
}

// TestAdminApplyINSERTCountDoesNotGrowLinearlyWithRowCount is ut-docs#1369's
// actual regression test through the real ApplyAdmin path (review finding
// 2): TestAdminApplyLargeTableBatchesAcrossMultipleChunks above proves
// correctness, but it passes just as well against the old one-exec-per-row
// code — it's a no-regression test, not a batching regression test. This
// one counts real INSERT statements prepared against the replica's
// connection (openCountingConnMatching, generalized from
// export_repo_querycount_test.go's SELECT-counting harness for
// ut-docs#229) while ApplyAdmin runs, for two different tax_codes row
// counts, and asserts the growth in INSERT count is small relative to the
// growth in row count — the old per-row code would show 1:1 growth (adding
// 450 rows costs 450 more INSERTs); the batched code costs only a few more
// chunk statements.
func TestAdminApplyINSERTCountDoesNotGrowLinearlyWithRowCount(t *testing.T) {
	ctx := context.Background()

	countFor := func(n int) int64 {
		primary := openMigratedDB(t, "primary.db")
		replicaPath := filepath.Join(t.TempDir(), "replica.db")
		replica, err := db.Open(replicaPath)
		if err != nil {
			t.Fatalf("open replica: %v", err)
		}
		t.Cleanup(func() { _ = replica.Close() })

		for i := 0; i < n; i++ {
			mustExec(t, primary,
				`INSERT INTO tax_codes (id, name, rate_basis_points, is_active) VALUES (?, ?, ?, 1)`,
				fmt.Sprintf("qc%04d", i), fmt.Sprintf("QC Tax %04d", i), int64(1000+i))
		}
		bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
		if err != nil {
			t.Fatalf("dump: %v", err)
		}

		counter := new(int64)
		countingDB := openCountingConnMatching(t, replicaPath, counter, "INSERT")
		if err := NewSyncAdminRepo(countingDB).ApplyAdmin(ctx, wireTrip(t, bundle)); err != nil {
			t.Fatalf("apply (n=%d): %v", n, err)
		}
		return atomic.LoadInt64(counter)
	}

	small := countFor(50)
	large := countFor(500)

	// Guard against the harness silently counting nothing (same defense as
	// export_repo_querycount_test.go's own large<3 check) — ApplyAdmin
	// touches dozens of adminTables, so a genuine batched apply always
	// issues at least a handful of INSERT statements.
	if small == 0 || large == 0 {
		t.Fatalf("harness counted 0 INSERTs (small=%d, large=%d) — it stopped counting, assertions below would be vacuous", small, large)
	}

	growth := large - small
	// 450 more tax_codes rows (500 vs 50) batched at chunkRows=100
	// (maxBatchPlaceholders=500 / 5 tax_codes columns) costs at most 5 more
	// chunk statements for that one table, plus noise from other tables'
	// batch boundaries shifting by at most a handful — 20 is a generous
	// bound that the OLD one-exec-per-row code would blow through by more
	// than an order of magnitude (it would cost 450 more INSERTs, one per
	// extra row).
	if growth > 20 {
		t.Fatalf("INSERT count grew by %d for 450 extra rows (small=%d, large=%d) — looks like one exec per row again, not chunked batching", growth, small, large)
	}
}
