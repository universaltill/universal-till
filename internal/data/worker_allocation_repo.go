package data

import (
	"context"
	"database/sql"
	"fmt"
)

// WorkerAllocation is one row of the shared worker-distribution ledger
// (ADR-0063, ut-docs#987) — the record-keeping primitive behind two
// independent statutory obligations: the UK Employment (Allocation of
// Tips) Act 2023 (ut-docs#964) and Turkey İş Kanunu 4857 art. 51 "yüzde
// usulü" (ut-docs#965). SourceID is a soft reference (a payment_id,
// sale_id, or — for a Turkey pool payout with no underlying bill line — a
// pool-batch identifier); deliberately not a foreign key — a soft
// reference cannot break or block on a catalog/customer/sale row that a
// later reset archives or a later cleanup removes, the same reasoning
// ADR-0063's Consequences give for why a report joining "received" to
// "allocated" must handle a source_id whose source row has since been
// archived, not assume a live join (ADR-0063 Decision 1).
type WorkerAllocation struct {
	ID          string
	SourceType  string // "tip" | "service_charge" | "yuzde_usulu_pool"
	SourceID    string
	CashierID   string
	AmountMinor int64
	AllocatedAt string
	Note        string
}

// InsertWorkerAllocation writes one worker_allocations row. Deliberately a
// narrow, single-purpose method — not an added argument on InsertSale or
// InsertPayment (ut-docs#976's existing signature-risk finding) — because a
// payout is its own event, often not in the same transaction as the sale
// or payment that generated the money (e.g. a shift-end distribution
// covering many sales at once). Callers insert one row per worker per
// payout event, in whatever transaction their own payout flow already has
// open.
func (r *POSRepo) InsertWorkerAllocation(ctx context.Context, tx *sql.Tx, id, sourceType, sourceID, cashierID string, amountMinor int64, allocatedAt, note string) error {
	_, err := r.exec(tx).ExecContext(ctx, `
INSERT INTO worker_allocations (id, source_type, source_id, cashier_id, amount_minor, allocated_at, note)
VALUES (?, ?, ?, ?, ?, ?, ?)
`, id, sourceType, sourceID, cashierID, amountMinor, allocatedAt, note)
	if err != nil {
		return fmt.Errorf("insert worker allocation: %w", err)
	}
	return nil
}

// WorkerAllocationSummary is one source_type's received-vs-allocated
// comparison over a date range, optionally scoped to one cashier — the
// shared shape ADR-0063 Decision 2 designs both #964's (UK) and #965's
// (Turkey) own consumer report to filter and display, each applying its
// own SourceType.
//
// Received and Allocated are NOT simply "equal means compliant" — they are
// measured on different clocks (Received: payment/collection time;
// Allocated: allocated_at, which ADR-0063 Decision 1 explicitly says "may
// be later than the sale/payment", e.g. a shift-end payout the next day)
// and, when cashierID is set, on different people (Received scopes by who
// rang the sale; Allocated scopes by who received the payout — ADR-0063
// Decision 2's own worker-request path reads Allocated for one worker,
// which is not "who processed the sale"). A single-day report showing
// Received != Allocated is routine and does not by itself mean money is
// missing — a caller (or a human) comparing the two fields needs a window
// wide enough to contain both the collection and its payout, and must
// treat a per-cashier scope as "this worker's payouts" vs "this shop's
// collections", not a like-for-like pair. For "yuzde_usulu_pool" the
// comparison is a tautology (see below) and must never be presented as a
// pass/fail check on its own.
type WorkerAllocationSummary struct {
	SourceType     string
	ReceivedMinor  int64
	AllocatedMinor int64
}

// WorkerAllocationsSummary computes one source_type's received-vs-allocated
// totals for [from, to] (matched against the shop's LOCAL calendar day,
// inclusive of both ends — the same date(..., 'localtime') convention
// dateRangeSummary uses for reports, ut-docs#869: a bare UTC date() match
// silently aggregates the wrong calendar day on any non-UTC host, and both
// this table's markets — Turkey UTC+3, UK UTC+0/+1 — are non-UTC), and
// optionally scoped to one cashierID ("" = every worker). This is the
// single shared query path ADR-0063 Decision 2 calls for: #964 and #965's
// own step-2 consumers each call this with their own sourceType filter
// rather than each building a separate query.
//
// "Allocated" always reads worker_allocations (this table) — the ledger
// this ADR adds. "Received" — what came in before it was distributed —
// depends on sourceType, since different money enters the till through
// different tables:
//   - "tip": payments.tip_amount, on completed sales only (a voided/
//     refunded sale's payment row is not deleted — pos.UpdateSaleStatus —
//     so an unfiltered sum would count money that was never actually
//     taken), and only tip_recipient = 'employee' (ADR-0061 Decision 3):
//     a 'business'-retained tip was never owed to a worker at all, so
//     counting it as "received" here would manufacture a permanent,
//     phantom shortfall against Allocated for any shop configured to keep
//     it. cashierID scopes it via payments' owning sale's cashier_id — a
//     receipt has no per-worker attribution of its own; "who this money
//     was FOR" is what the allocation rows (the "allocated" side) record.
//   - "yuzde_usulu_pool": Turkey collects a percentage with no underlying
//     bill line (ADR-0062/ut-docs#962 forbid a Turkey service-charge row
//     outright) and this ADR does not invent the collection-side
//     mechanism (ut-docs#965's own remaining scope) — so "received" for a
//     pool is the same worker_allocations rows summed by their shared
//     source_id batch marker (ADR-0063 Decision 3): the ledger is, for
//     now, its own evidence that a pool was distributed in full at the
//     moment it's recorded — Received == Allocated here BY CONSTRUCTION,
//     a tautology, not a passed compliance check; a step-2 consumer must
//     not render it as one. cashierID has no effect on the received total
//     for this source_type (a pool's "received" side is the whole pool,
//     not one worker's share) but continues to scope "allocated".
//   - "service_charge": needs sale_charges (ADR-0062, migration only —
//     ut-docs#984 landed the schema, but ut-docs#985's step 2 is what
//     wires anything to actually WRITE a row, so the table exists but is
//     empty as of this card). Returns ReceivedMinor: 0 with Allocated
//     still correctly computed, rather than failing outright or joining a
//     table that would return nothing anyway — #964 does not consume this
//     source_type (UK's own obligation is "tip"/service-charge-under-UK-
//     law, i.e. this codebase's "tip" bucket per ADR-0061), so nothing
//     downstream depends on this being non-zero today. Revisit once
//     ut-docs#985 lands.
//
// Deliberate deviation from ADR-0063 Decision 2's literal wording ("joins
// ... by source_id"): this does NOT join Received to Allocated by
// source_id. A source_id join would only count a tip/pool that already has
// an allocation row, making Received <= Allocated by construction and
// hiding exactly the shortfall this report exists to surface. Independent
// review confirmed this is the better reading; recorded here per ut-docs
// review process rather than as a silent departure from the ADR's text.
func (r *POSRepo) WorkerAllocationsSummary(ctx context.Context, from, to, cashierID, sourceType string) (WorkerAllocationSummary, error) {
	out := WorkerAllocationSummary{SourceType: sourceType}
	if sourceType == "" {
		return out, fmt.Errorf("worker allocations summary: source_type is required")
	}

	allocatedArgs := []any{sourceType, from, to}
	allocatedQuery := `
SELECT COALESCE(SUM(amount_minor), 0) FROM worker_allocations
WHERE source_type = ? AND date(allocated_at, 'localtime') BETWEEN date(?) AND date(?)`
	if cashierID != "" {
		allocatedQuery += ` AND cashier_id = ?`
		allocatedArgs = append(allocatedArgs, cashierID)
	}
	if err := r.db.QueryRowContext(ctx, allocatedQuery, allocatedArgs...).Scan(&out.AllocatedMinor); err != nil {
		return out, fmt.Errorf("worker allocations summary: allocated: %w", err)
	}

	switch sourceType {
	case "tip":
		// status = 'completed': a voided/refunded sale's payment row is
		// never deleted (pos.UpdateSaleStatus), so an unfiltered sum would
		// count a tip on money that was never actually taken. tip_recipient
		// = 'employee': a 'business'-retained tip (ADR-0061 Decision 3) was
		// never owed to a worker, so it must not manufacture a phantom
		// shortfall against Allocated. See the type's own doc comment for
		// why cashierID scopes via the sale's cashier, not the allocation's.
		receivedQuery := `
SELECT COALESCE(SUM(p.tip_amount), 0) FROM payments p
JOIN sales s ON s.id = p.sale_id
WHERE date(p.paid_at, 'localtime') BETWEEN date(?) AND date(?)
  AND s.status = 'completed'
  AND COALESCE(p.tip_recipient, 'employee') = 'employee'`
		receivedArgs := []any{from, to}
		if cashierID != "" {
			receivedQuery += ` AND s.cashier_id = ?`
			receivedArgs = append(receivedArgs, cashierID)
		}
		if err := r.db.QueryRowContext(ctx, receivedQuery, receivedArgs...).Scan(&out.ReceivedMinor); err != nil {
			return out, fmt.Errorf("worker allocations summary: received (tip): %w", err)
		}
	case "yuzde_usulu_pool":
		// No separate collection record exists yet (see doc comment above)
		// — the ledger's own rows are the only evidence, so "received" for
		// a pool matches "allocated" by construction, deliberately without
		// the cashier_id filter (a pool's received side is the whole
		// pool's collection, not one worker's share).
		receivedQuery := `
SELECT COALESCE(SUM(amount_minor), 0) FROM worker_allocations
WHERE source_type = 'yuzde_usulu_pool' AND date(allocated_at, 'localtime') BETWEEN date(?) AND date(?)`
		if err := r.db.QueryRowContext(ctx, receivedQuery, from, to).Scan(&out.ReceivedMinor); err != nil {
			return out, fmt.Errorf("worker allocations summary: received (yuzde_usulu_pool): %w", err)
		}
	case "service_charge":
		// sale_charges exists (ADR-0062, ut-docs#984) but nothing writes to
		// it until ut-docs#985 lands — see doc comment above. ReceivedMinor
		// stays 0; AllocatedMinor above is still correct.
	default:
		return out, fmt.Errorf("worker allocations summary: unsupported source_type %q", sourceType)
	}

	return out, nil
}
