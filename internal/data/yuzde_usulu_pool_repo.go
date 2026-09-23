package data

import (
	"context"
	"database/sql"
	"fmt"
)

// YuzdeUsuluPoolCollection is one operator-entered "we collected X today
// under yüzde usulü" record (ut-docs#988, migration 037) — the
// collection-side mechanism ADR-0063 Decision 3 explicitly deferred.
//
// Why it needs a table of its own rather than reusing an existing one:
// Turkey collects a yüzde usulü percentage with NO customer-facing bill
// line at all (a Turkey service-charge line is forbidden outright —
// ut-docs#962, common.ServiceChargeForbidden), so unlike the UK's tips
// (payments.tip_amount) or a service charge (sale_charges), there is no
// sale, payment or charge row a pool collection can be derived from. Until
// this record existed, worker_allocations was its own only evidence that a
// pool had ever been collected, which made WorkerAllocationsSummary's
// "received" for 'yuzde_usulu_pool' a tautology (see that function's doc
// comment).
//
// RecordedBy is the manager's user id who entered the collection — a soft
// reference with no foreign key to users(id), the same reasoning
// WorkerAllocation.SourceID's own doc comment gives: an append-only
// statutory record must survive a later user deletion, not break on it.
//
// AmountMinor is money (internal/money.Money) as a raw minor-unit integer
// at this DB boundary, the same convention as WorkerAllocation.AmountMinor.
//
// BasisNote is free text describing how the pool is meant to be split
// ("kitchen 30% / floor 70%") — the collection-side twin of
// WorkerAllocation.Note. Nothing parses or enforces it: this feature
// records what an operator says happened, it does not decide policy
// (ADR-0063 Decision 4).
type YuzdeUsuluPoolCollection struct {
	ID          string
	RecordedBy  string
	CollectedAt string
	BasisNote   string
	AmountMinor int64
}

// InsertYuzdeUsuluPoolCollection writes one yuzde_usulu_pool_collections
// row. Narrow and single-purpose, deliberately mirroring
// InsertWorkerAllocation's own shape (and its reasoning — ut-docs#976's
// signature-risk finding): a pool collection is its own event, recorded by
// a manager at the end of a trading day, not a field bolted onto
// InsertSale/InsertPayment — there is no sale or payment it belongs to in
// the first place.
//
// local_date is precomputed here, from the same collectedAt literal, via
// date(?, 'localtime') — identical to InsertWorkerAllocation's handling of
// allocated_at (ut-docs#869/#1342): a 'localtime' expression cannot back an
// index (SQLite treats it as non-deterministic), and a bare UTC date()
// match on the read side would silently aggregate the wrong calendar day
// on any non-UTC host — which Turkey (UTC+3), this table's one market,
// always is. The COALESCE fallback to an empty string keeps the NOT NULL
// column satisfied if
// SQLite cannot parse collectedAt at all, exactly as the worker_allocations
// insert does, rather than failing the whole write.
func (r *POSRepo) InsertYuzdeUsuluPoolCollection(ctx context.Context, tx *sql.Tx, id, recordedBy string, amountMinor int64, collectedAt, basisNote string) error {
	_, err := r.exec(tx).ExecContext(ctx, `
INSERT INTO yuzde_usulu_pool_collections (id, amount_minor, collected_at, basis_note, recorded_by, local_date)
VALUES (?, ?, ?, ?, ?, COALESCE(date(?, 'localtime'), ''))
`, id, amountMinor, collectedAt, basisNote, recordedBy, collectedAt)
	if err != nil {
		return fmt.Errorf("insert yuzde usulu pool collection: %w", err)
	}
	return nil
}

// ListYuzdeUsuluPoolCollections returns every pool collection recorded in
// [from, to] — matched against the shop's LOCAL calendar day, inclusive of
// both ends, through the precomputed local_date column, the same convention
// (and for the same reason) as ListWorkerAllocations/WorkerAllocationsSummary:
// see InsertYuzdeUsuluPoolCollection above and ut-docs#869.
//
// Ordered by collected_at DESC — most recent collection first, matching
// ListWorkerAllocations' own allocated_at DESC ordering, which is the order
// a manager reviewing the period (or picking which pool a distribution is
// coming out of) expects.
//
// There is deliberately no cashier/worker scope parameter, unlike
// ListWorkerAllocations: a collection is the WHOLE pool as it came in,
// before any split — "which worker" only becomes a question on the
// distribution side (worker_allocations).
func (r *POSRepo) ListYuzdeUsuluPoolCollections(ctx context.Context, from, to string) ([]YuzdeUsuluPoolCollection, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, amount_minor, collected_at, basis_note, recorded_by
FROM yuzde_usulu_pool_collections
WHERE local_date BETWEEN date(?) AND date(?)
ORDER BY collected_at DESC`, from, to)
	if err != nil {
		return nil, fmt.Errorf("list yuzde usulu pool collections: %w", err)
	}
	defer rows.Close()

	var out []YuzdeUsuluPoolCollection
	for rows.Next() {
		var c YuzdeUsuluPoolCollection
		if err := rows.Scan(&c.ID, &c.AmountMinor, &c.CollectedAt, &c.BasisNote, &c.RecordedBy); err != nil {
			return nil, fmt.Errorf("list yuzde usulu pool collections: scan: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list yuzde usulu pool collections: %w", err)
	}
	return out, nil
}

// YuzdeUsuluPoolCollectionsTotal sums every pool collection in [from, to] —
// the "received" side of WorkerAllocationsSummary's 'yuzde_usulu_pool'
// comparison, and the reason that comparison stopped being a tautology
// (ut-docs#988). Same local_date range convention as the list query above.
//
// COALESCE(...,0): a period with no collection at all is a real, ordinary
// answer (zero), not an error or a NULL the caller has to special-case —
// the same shape WorkerAllocationsSummary's own totals use.
func (r *POSRepo) YuzdeUsuluPoolCollectionsTotal(ctx context.Context, from, to string) (int64, error) {
	var total int64
	err := r.db.QueryRowContext(ctx, `
SELECT COALESCE(SUM(amount_minor), 0) FROM yuzde_usulu_pool_collections
WHERE local_date BETWEEN date(?) AND date(?)`, from, to).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("yuzde usulu pool collections total: %w", err)
	}
	return total, nil
}

// GetYuzdeUsuluPoolCollection looks one collection up by id. The bool is
// "found" — the same found-bool convention AuthRepo.GetUser uses (and that
// registerWorkerAllocationAPI's own cashier_id validation already consumes),
// so a caller can tell "no such pool" (a 400 on an operator-supplied
// pool_id) apart from a real query failure (a 500) without string-matching
// sql.ErrNoRows.
func (r *POSRepo) GetYuzdeUsuluPoolCollection(ctx context.Context, id string) (YuzdeUsuluPoolCollection, bool, error) {
	var c YuzdeUsuluPoolCollection
	err := r.db.QueryRowContext(ctx, `
SELECT id, amount_minor, collected_at, basis_note, recorded_by
FROM yuzde_usulu_pool_collections WHERE id = ?`, id).
		Scan(&c.ID, &c.AmountMinor, &c.CollectedAt, &c.BasisNote, &c.RecordedBy)
	if err == sql.ErrNoRows {
		return YuzdeUsuluPoolCollection{}, false, nil
	}
	if err != nil {
		return YuzdeUsuluPoolCollection{}, false, fmt.Errorf("get yuzde usulu pool collection: %w", err)
	}
	return c, true, nil
}
