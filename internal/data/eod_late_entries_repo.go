package data

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"
)

// EODLateTill is one other till's rows that a Z counted although they were
// made before that Z's window opened (ut-docs#5769, ADR-0066 amendment).
// Gross/RefundTotal/ClearedBasketTotal are minor-unit sums in a stored-JSON
// DTO, like EODReport.Gross; nothing does arithmetic on them in Go.
// ClearedBasketTotal is a pointer so a real 0 sum (recorded) differs from
// "not recorded" (nil: a Z archived before ut-docs#5776, or no late cleared
// baskets). EarliestOrigin is this till's own oldest origin time (local-offset
// RFC3339, "" when unparseable or on a Z archived before ut-docs#5776).
type EODLateTill struct {
	TillID             string `json:"till_id"`
	Name               string `json:"name"`
	SalesCount         int    `json:"sales_count"`
	Gross              int64  `json:"gross"`
	RefundCount        int    `json:"refund_count"`
	RefundTotal        int64  `json:"refund_total"`
	NoSaleCount        int    `json:"no_sale_count"`
	ClearedBasketCount int    `json:"cleared_basket_count"`
	ClearedBasketTotal *int64 `json:"cleared_basket_total,omitempty"`
	EarliestOrigin     string `json:"earliest_origin,omitempty"`
}

// EODLateEntries is the "recorded late from other tills" note stored on the
// archived Z. EarliestOrigin is the oldest origin time among the late rows,
// local-offset RFC3339 like EODReport.From.
type EODLateEntries struct {
	EarliestOrigin string        `json:"earliest_origin"`
	Tills          []EODLateTill `json:"tills"`
}

// LateEntriesForInstantWindow lists, per journaling till, the rows counted in
// [from, to) by their arrival time (saleArrivalCol / eventArrivalCol) whose
// origin created_at is before `from`. nil, nil on a zero `from` (first-ever
// close: no previous close to be late against) or when nothing is late.
func (r *POSRepo) LateEntriesForInstantWindow(ctx context.Context, from, to time.Time) (*EODLateEntries, error) {
	if from.IsZero() {
		return nil, nil
	}
	fromStr, _ := windowArgs(from, to)
	tills := map[string]*EODLateTill{}
	get := func(id string) *EODLateTill {
		t, ok := tills[id]
		if !ok {
			t = &EODLateTill{TillID: id}
			tills[id] = t
		}
		return t
	}
	var earliest string            // min origin as "YYYY-MM-DD HH:MM:SS" (datetime form, sorts as text)
	tillMin := map[string]string{} // same datetime form, per till
	note := func(id string, min sql.NullString) {
		if !min.Valid || min.String == "" {
			return
		}
		if earliest == "" || min.String < earliest {
			earliest = min.String
		}
		if cur := tillMin[id]; cur == "" || min.String < cur {
			tillMin[id] = min.String
		}
	}

	swin, sargs := instantWindow(saleArrivalCol("s"), from, to)
	// An own sale marked at a promote (recorded_at, ut-docs#5777) is listed
	// under this till's own roster id and name: ClearReplicaIdentity keeps
	// sync.till_id as till_identity.cloud_id, the id the old main till filed
	// it under.
	const key = `CASE WHEN COALESCE(s.till_id, '') = '' THEN COALESCE((SELECT value FROM settings WHERE key = 'till_identity.cloud_id'), '') ELSE s.till_id END`
	// Only an own row (empty till_id) falls back to this till's name; another
	// till missing from the roster keeps the roster's (possibly empty) name.
	const name = `CASE WHEN COALESCE(s.till_id, '') = '' THEN COALESCE(NULLIF(t.name, ''), (SELECT value FROM settings WHERE key = 'till.name'), '') ELSE COALESCE(t.name, '') END`
	rows, err := r.db.QueryContext(ctx, `
SELECT `+key+`, `+name+`, s.sale_type, COUNT(*), COALESCE(SUM(s.total), 0), MIN(datetime(s.created_at))
FROM sales s
LEFT JOIN tills t ON t.id = `+key+`
WHERE s.status = 'completed' AND (COALESCE(s.till_id, '') <> '' OR s.recorded_at IS NOT NULL) AND `+swin+`
  AND datetime(s.created_at) < datetime(?)
GROUP BY `+key+`, s.sale_type`, append(sargs, fromStr)...)
	if err != nil {
		return nil, fmt.Errorf("late sales: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, name, typ string
		var n int
		var gross int64
		var min sql.NullString
		if err := rows.Scan(&id, &name, &typ, &n, &gross, &min); err != nil {
			return nil, fmt.Errorf("scan late sales: %w", err)
		}
		t := get(id)
		t.Name = name
		if typ == "return" {
			t.RefundCount, t.RefundTotal = n, gross
		} else {
			t.SalesCount, t.Gross = n, gross
		}
		note(id, min)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, ev := range []struct{ table, field, amount string }{
		{"no_sale_events", "no", "0"},
		{"cleared_baskets", "cl", "COALESCE(SUM(e.total_minor), 0)"},
	} {
		ewin, eargs := instantWindow(eventArrivalCol("e"), from, to)
		erows, err := r.db.QueryContext(ctx, `
SELECT e.till_id, COALESCE(t.name, ''), COUNT(*), `+ev.amount+`, MIN(datetime(e.created_at))
FROM `+ev.table+` e
LEFT JOIN tills t ON t.id = e.till_id
WHERE COALESCE(e.till_id, '') <> '' AND `+ewin+`
  AND datetime(e.created_at) < datetime(?)
GROUP BY e.till_id`, append(eargs, fromStr)...)
		if err != nil {
			if ev.field == "no" {
				// No-sale opens are best-effort on the close (ut-docs#5753):
				// a read failure there must never fail it.
				continue
			}
			return nil, fmt.Errorf("late %s: %w", ev.table, err)
		}
		for erows.Next() {
			var id, name string
			var n int
			var amt int64
			var min sql.NullString
			if err := erows.Scan(&id, &name, &n, &amt, &min); err != nil {
				erows.Close()
				return nil, fmt.Errorf("scan late %s: %w", ev.table, err)
			}
			t := get(id)
			if t.Name == "" {
				t.Name = name
			}
			if ev.field == "no" {
				t.NoSaleCount = n
			} else {
				t.ClearedBasketCount = n
				v := amt
				t.ClearedBasketTotal = &v
			}
			note(id, min)
		}
		err = erows.Err()
		erows.Close()
		if err != nil {
			return nil, err
		}
	}
	if len(tills) == 0 {
		return nil, nil
	}
	out := &EODLateEntries{}
	for id, t := range tills {
		t.EarliestOrigin = lateLocalRFC3339(tillMin[id])
		out.Tills = append(out.Tills, *t)
	}
	sort.Slice(out.Tills, func(i, j int) bool { return out.Tills[i].TillID < out.Tills[j].TillID })
	out.EarliestOrigin = lateLocalRFC3339(earliest)
	return out, nil
}

// lateLocalRFC3339 turns a SQLite "YYYY-MM-DD HH:MM:SS" UTC datetime into a
// local-offset RFC3339 string; "" when it does not parse.
func lateLocalRFC3339(s string) string {
	ts, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.UTC)
	if err != nil {
		return ""
	}
	return ts.Local().Format(time.RFC3339)
}

// HoldWriteLock takes SQLite's write lock (every BeginTx here is BEGIN
// IMMEDIATE, internal/db/db.go) and returns the release func. generateEOD
// holds it from the moment it takes `to` until every window read of the Z is
// done: an apply (sale replay, journaled no-sale/cleared basket) either
// committed before (stamp < to, visible to the reads) or runs after (stamp >=
// to, next Z), so a row can never be stamped before `to` yet commit after the
// Z read its stream (ut-docs#5769, ADR-0066 amendment). Reads stay on the
// pool: WAL readers are not blocked by the held writer. It writes nothing;
// release rolls back.
//
// Writer-stall contract: while held, every other writer (checkout, sync
// replay, journal apply) blocks up to the 5 s busy_timeout and then fails
// with SQLITE_BUSY. The caller MUST bound the hold with a ctx deadline well
// below that (generateEOD: 4 s; the tx rolls back when ctx ends) and fail
// the whole operation, never write, if it expires. Not for use across a write of the holder's own
// (ArchiveReport): release first.
func (r *POSRepo) HoldWriteLock(ctx context.Context) (release func(), err error) {
	// A pool capped at one connection (a test configuration; db.Open sets no
	// cap) cannot hold a tx AND read on the pool: that would deadlock. There
	// the hold degrades to a no-op.
	if r.db.Stats().MaxOpenConnections == 1 {
		return func() {}, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("hold write lock: %w", err)
	}
	return func() { _ = tx.Rollback() }, nil
}
