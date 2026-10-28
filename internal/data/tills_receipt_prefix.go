package data

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// TillNoHighWaterSettingsKey is the highest till number (receipt prefix
// T<n>-) the shop's main till has ever granted (ut-docs#5074 S0). It is a
// shop setting, deliberately outside the per-till "sync." family, so it
// travels to every till in the admin bundle and a till promoted to main
// till continues the sequence instead of starting again.
const TillNoHighWaterSettingsKey = "shop.till_no_high_water"

// maxTillNo is the largest number tillPrefixRe accepts.
const maxTillNo = 9999

// tillPrefixRe is a joined till's receipt prefix, T<n>- with n >= 2 (the
// main till is till 1 and has none).
var tillPrefixRe = regexp.MustCompile(`^T([2-9]|[1-9][0-9]{1,3})-$`)

// tillNoOf returns n for a valid T<n>- prefix, else 0.
func tillNoOf(prefix string) int {
	m := tillPrefixRe.FindStringSubmatch(strings.TrimSpace(prefix))
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// EnrolTill enrols a till and grants it its receipt prefix in ONE
// transaction (ut-docs#5074 S0; ut-docs#5058 UT-5 for the re-join rule):
//
//   - requestedPrefix is a re-joining till's own prefix ("" for a till that
//     never joined). It is kept when valid and nothing on this main till
//     shows it in use (another till holds it, a sale or an archived sale
//     carries it, or it is this till's own), so a re-joining till goes on
//     numbering where it left off and its queued sales keep their numbers.
//   - Otherwise the number is one past everything ever granted
//     (TillNoHighWaterSettingsKey), every prefix in use, and the till count
//     (the pre-092 rule, a floor for tills enrolled before prefixes were
//     recorded). A revoked or withdrawn till's number is therefore never
//     granted to another till: two tills on one prefix mint the same
//     receipt_no, and the journal replay of the second is refused for good
//     on UNIQUE(sales.receipt_no).
//
// The transaction takes the write lock first (the DSN's _txlock=immediate),
// so two tills enrolling at once can never be given the same number.
func (r *TillsRepo) EnrolTill(ctx context.Context, name, bearerHash, role, requestedPrefix string) (id string, tillNo int, err error) {
	if !ValidTillRole(role) {
		return "", 0, fmt.Errorf("enrol till: invalid role %q", role)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", 0, fmt.Errorf("enrol till: begin: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	id = uuid.NewString()
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO tills (id, name, bearer_hash, role) VALUES (?, ?, ?, ?)`, id, name, bearerHash, role); err != nil {
		return "", 0, fmt.Errorf("enrol till: insert: %w", err)
	}
	used, err := receiptPrefixesInUse(ctx, tx)
	if err != nil {
		return "", 0, fmt.Errorf("enrol till: %w", err)
	}
	var hwRaw string
	if err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, TillNoHighWaterSettingsKey).Scan(&hwRaw); err != nil && err != sql.ErrNoRows {
		return "", 0, fmt.Errorf("enrol till: read %s: %w", TillNoHighWaterSettingsKey, err)
	}
	err = nil
	highWater, _ := strconv.Atoi(strings.TrimSpace(hwRaw)) // unset or unreadable: the scan below still counts
	if n := tillNoOf(requestedPrefix); n > 0 && !used[fmt.Sprintf("T%d-", n)] {
		tillNo = n
	} else {
		// The till count includes the row inserted above, so the old rule's
		// len(tills)+1 is count+1.
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tills`).Scan(&count); err != nil {
			return "", 0, fmt.Errorf("enrol till: count tills: %w", err)
		}
		last := max(highWater, count, 1)
		for p := range used {
			last = max(last, tillNoOf(p))
		}
		tillNo = last + 1
	}
	if tillNo > maxTillNo {
		return "", 0, fmt.Errorf("enrol till: no till number left (highest granted %d)", tillNo-1)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tills SET receipt_prefix = ? WHERE id = ?`, fmt.Sprintf("T%d-", tillNo), id); err != nil {
		return "", 0, fmt.Errorf("enrol till: record receipt prefix: %w", err)
	}
	if tillNo > highWater {
		if _, err = tx.ExecContext(ctx, `
INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			TillNoHighWaterSettingsKey, strconv.Itoa(tillNo), time.Now().UTC()); err != nil {
			return "", 0, fmt.Errorf("enrol till: record %s: %w", TillNoHighWaterSettingsKey, err)
		}
	}
	if err = tx.Commit(); err != nil {
		return "", 0, fmt.Errorf("enrol till: commit: %w", err)
	}
	return id, tillNo, nil
}

// receiptPrefixesInUse is every T<n>- receipt prefix this main till knows to
// be taken: granted to an enrolled till, carried by a sale it holds or one
// moved out by "Clear transaction history" (a till enrolled before migration
// 092, or one since removed), or this till's own (a till promoted to main
// till keeps its sync.receipt_prefix), or seen in its read-only copy of the
// other tills' sales (mirror_sales). ut-docs#5058, ut-docs#5074.
func receiptPrefixesInUse(ctx context.Context, q dbExecutor) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, `
SELECT receipt_prefix FROM tills WHERE receipt_prefix != ''
UNION
SELECT DISTINCT substr(receipt_no, 1, instr(receipt_no, '-')) FROM sales
WHERE receipt_no GLOB 'T[0-9]*-*'
UNION
SELECT DISTINCT substr(receipt_no, 1, instr(receipt_no, '-')) FROM sales_archive
WHERE receipt_no GLOB 'T[0-9]*-*'
UNION
SELECT value FROM settings WHERE key = 'sync.receipt_prefix' AND value != ''`)
	if err != nil {
		return nil, fmt.Errorf("receipt prefixes in use: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("scan receipt prefix: %w", err)
		}
		out[strings.TrimSpace(p)] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// ut-docs#5074 §3.6: and every prefix in this till's read-only sales copy.
	copied, err := mirrorReceiptPrefixesInUse(ctx, q)
	if err != nil {
		return nil, err
	}
	for _, p := range copied {
		out[strings.TrimSpace(p)] = true
	}
	return out, nil
}
