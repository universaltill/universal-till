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

// tillPrefixRe is a joined till's receipt prefix with n >= 2 (the main till
// is till 1): the legacy T<n>- or, since ut-docs#6663, the plain <n>- the
// main till grants now. Both forms share one never-reused number space.
var tillPrefixRe = regexp.MustCompile(`^T?([2-9]|[1-9][0-9]{1,3})-$`)

// firstTillPrefixRe is the first main till's own prefix, "1-" (ut-docs#6663).
var firstTillPrefixRe = regexp.MustCompile(`^1-$`)

// FirstMainTillPrefix is the receipt prefix of a shop's first main till.
const FirstMainTillPrefix = "1-"

// tillNoOf returns n for a valid joined-till prefix (T<n>- or <n>-), else 0.
func tillNoOf(prefix string) int {
	m := tillPrefixRe.FindStringSubmatch(strings.TrimSpace(prefix))
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// TillNumberOfPrefix is the till number a receipt prefix stands for, where a
// number is needed (the order-number lane): T<n>- and <n>- are till n; "",
// "1-", the replacement-main forms (M<8hex>-, cloud letters) and a restored
// copy's R<4hex>- count as till 1.
func TillNumberOfPrefix(prefix string) int {
	if n := tillNoOf(prefix); n > 0 {
		return n
	}
	return 1
}

// cloudSeriesPrefixRe is a replacement main till's cloud-issued prefix:
// 1 to 3 letters of the cloud's alphabet (no I, O, M, R, T), then "-".
var cloudSeriesPrefixRe = regexp.MustCompile(`^[A-HJ-LNPQSU-Z]{1,3}-$`)

// IsCloudSeriesPrefix reports whether p is a prefix the cloud can grant a
// replacement main till at pairing (ut-docs#6663). internal/db mirrors it.
func IsCloudSeriesPrefix(p string) bool { return cloudSeriesPrefixRe.MatchString(p) }

// EnrolTillPrefix enrols a till and grants it its receipt prefix in ONE
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
// It also returns the receipt prefix granted
// ("2-", or a re-joining till's own T<n>-), which the till must adopt.
func (r *TillsRepo) EnrolTillPrefix(ctx context.Context, name, bearerHash, role, requestedPrefix string) (id string, tillNo int, prefix string, err error) {
	if !ValidTillRole(role) {
		return "", 0, "", fmt.Errorf("enrol till: invalid role %q", role)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", 0, "", fmt.Errorf("enrol till: begin: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	id = uuid.NewString()
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO tills (id, name, bearer_hash, role, bearer_issued_at) VALUES (?, ?, ?, ?, ?)`,
		id, name, bearerHash, role, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return "", 0, "", fmt.Errorf("enrol till: insert: %w", err)
	}
	used, err := receiptPrefixesInUse(ctx, tx)
	if err != nil {
		return "", 0, "", fmt.Errorf("enrol till: %w", err)
	}
	var hwRaw string
	if err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, TillNoHighWaterSettingsKey).Scan(&hwRaw); err != nil && err != sql.ErrNoRows {
		return "", 0, "", fmt.Errorf("enrol till: read %s: %w", TillNoHighWaterSettingsKey, err)
	}
	err = nil
	highWater, _ := strconv.Atoi(strings.TrimSpace(hwRaw)) // unset or unreadable: the scan below still counts
	granted := ""
	if n := tillNoOf(requestedPrefix); n > 0 && !used[fmt.Sprintf("T%d-", n)] && !used[fmt.Sprintf("%d-", n)] {
		// A till keeps the prefix it has for life, T<n>- or <n>- (it is also
		// its invoice series), never renamed on re-join.
		tillNo = n
		granted = strings.TrimSpace(requestedPrefix)
	} else {
		// The till count includes the row inserted above, so the old rule's
		// len(tills)+1 is count+1.
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tills`).Scan(&count); err != nil {
			return "", 0, "", fmt.Errorf("enrol till: count tills: %w", err)
		}
		last := max(highWater, count, 1)
		for p := range used {
			last = max(last, tillNoOf(p))
		}
		tillNo = last + 1
	}
	if tillNo > maxTillNo {
		return "", 0, "", fmt.Errorf("enrol till: no till number left (highest granted %d)", tillNo-1)
	}
	if granted == "" {
		granted = fmt.Sprintf("%d-", tillNo)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tills SET receipt_prefix = ? WHERE id = ?`, granted, id); err != nil {
		return "", 0, "", fmt.Errorf("enrol till: record receipt prefix: %w", err)
	}
	if tillNo > highWater {
		if _, err = tx.ExecContext(ctx, `
INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			TillNoHighWaterSettingsKey, strconv.Itoa(tillNo), time.Now().UTC()); err != nil {
			return "", 0, "", fmt.Errorf("enrol till: record %s: %w", TillNoHighWaterSettingsKey, err)
		}
	}
	if err = tx.Commit(); err != nil {
		return "", 0, "", fmt.Errorf("enrol till: commit: %w", err)
	}
	return id, tillNo, granted, nil
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
WHERE receipt_no GLOB 'T[0-9]*-*' OR receipt_no GLOB '[1-9]*-*'
UNION
SELECT DISTINCT substr(receipt_no, 1, instr(receipt_no, '-')) FROM sales_archive
WHERE receipt_no GLOB 'T[0-9]*-*' OR receipt_no GLOB '[1-9]*-*'
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

// ReplacementMainTillPrefix is the receipt prefix (and so the invoice series)
// of a main till that replaced another one (ut-docs#5058 T4). The original
// main till has no prefix, so a replacement on prefix "" would repeat every
// receipt and invoice number of the till it replaced. The prefix is derived
// from the new till's device id: always M<8 hex>-, never equal to the
// replaced till's own derived prefix (the next 8 hex characters are taken
// if it would be). The leading M cannot match T<n>- (tillPrefixRe) nor the
// provisional R<4 hex>- of a restored copy, and is never empty.
func ReplacementMainTillPrefix(newDeviceID, oldDeviceID string) string {
	hexOf := func(id string) string {
		h := strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(id, "till-"), "-", ""))
		var b strings.Builder
		for _, r := range h {
			if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') {
				b.WriteRune(r)
			}
		}
		return (b.String() + strings.Repeat("0", 32))[:32]
	}
	n, o := hexOf(newDeviceID), hexOf(oldDeviceID)
	if oldDeviceID == "" {
		return "M" + n[:8] + "-"
	}
	for i := 0; i+8 <= len(n); i += 8 {
		if n[i:i+8] != o[:8] {
			return "M" + n[i:i+8] + "-"
		}
	}
	return "M" + n[:8] + "-"
}

var replacementMainTillPrefixRe = regexp.MustCompile(`^M[0-9a-f]{8}-$`)

// IsReplacementMainTillPrefix reports whether p is a replacement main till's
// prefix: the local M<8hex>- ReplacementMainTillPrefix returns, or the
// cloud-issued letter form (IsCloudSeriesPrefix, ut-docs#6663). Both are kept
// across a restore. internal/db mirrors it (isReplacementMainTillPrefix; a
// test pins the two together).
func IsReplacementMainTillPrefix(p string) bool {
	return replacementMainTillPrefixRe.MatchString(p) || IsCloudSeriesPrefix(p)
}
