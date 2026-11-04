package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/universaltill/universal-till/internal/cloudnotice"
)

// Notices the cloud sends to this till (ADR-0176, ut-docs#5366), kept in
// cloud_notices (migration 096). All SQL for the table lives here. The sell
// path never reads it; only the bell, inbox, toast and strip do.

// Bounds from ADR-0176 §3. The cloud caps a pull at 20 items, so these are a
// defence against a bad cloud, not a working limit.
const (
	noticeMaxRows  = 100
	noticeMaxBytes = 256 << 10
)

// noticeTimeLayout is fixed-width so stored timestamps compare as strings.
// It is valid RFC 3339 UTC, which is what the wire wants for acks.
const noticeTimeLayout = "2006-01-02T15:04:05.000000Z"

func noticeFmt(t time.Time) string { return t.UTC().Format(noticeTimeLayout) }

func noticeParse(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// CloudNotice is one held notice, in the language the till asked for.
type CloudNotice struct {
	ID, SenderKind, SenderName, Severity, Lang, Title, Body string
	Rev                                                     int
	LinkJSON                                                string
	StartsAt, ExpiresAt, CreatedAt, ReceivedAt              time.Time
	ReadAt, DismissedAt, AckedAt, ToastedAt                 *time.Time
}

// Unread: not opened and not dismissed.
func (n CloudNotice) Unread() bool { return n.ReadAt == nil && n.DismissedAt == nil }

// NoticeTab selects the inbox tab.
type NoticeTab int

// Inbox tabs. (Past is not offered: ADR-0176 §3 deletes a row at expires_at.)
const (
	NoticeAll NoticeTab = iota
	NoticeUnread
)

// NoticeAck is one entry of the request's acks array (ADR-0176 §4).
type NoticeAck struct {
	ID          string `json:"id"`
	DeliveredAt string `json:"delivered_at,omitempty"`
	ReadAt      string `json:"read_at,omitempty"`
	DismissedAt string `json:"dismissed_at,omitempty"`
	AckedAt     string `json:"acked_at,omitempty"`
}

// CloudNoticeRepo owns cloud_notices.
type CloudNoticeRepo struct {
	db                *sql.DB
	maxRows, maxBytes int
}

// NewCloudNoticeRepo returns the repository.
func NewCloudNoticeRepo(db *sql.DB) *CloudNoticeRepo {
	return &CloudNoticeRepo{db: db, maxRows: noticeMaxRows, maxBytes: noticeMaxBytes}
}

const noticeCols = `id, rev, sender_kind, sender_name, severity, lang, title, body, link_json,
 starts_at, expires_at, created_at, received_at, read_at, dismissed_at, acked_at, toasted_at`

type noticeScanner interface{ Scan(...any) error }

func scanNotice(s noticeScanner) (CloudNotice, error) {
	var n CloudNotice
	var st, ex, cr, rc string
	var rd, dm, ak, ts sql.NullString
	if err := s.Scan(&n.ID, &n.Rev, &n.SenderKind, &n.SenderName, &n.Severity, &n.Lang, &n.Title, &n.Body, &n.LinkJSON,
		&st, &ex, &cr, &rc, &rd, &dm, &ak, &ts); err != nil {
		return n, err
	}
	n.StartsAt, n.ExpiresAt, n.CreatedAt, n.ReceivedAt = noticeParse(st), noticeParse(ex), noticeParse(cr), noticeParse(rc)
	opt := func(v sql.NullString) *time.Time {
		if !v.Valid || v.String == "" {
			return nil
		}
		t := noticeParse(v.String)
		return &t
	}
	n.ReadAt, n.DismissedAt, n.AckedAt, n.ToastedAt = opt(rd), opt(dm), opt(ak), opt(ts)
	return n, nil
}

// noticeLive is the WHERE fragment every read shares: started, not expired.
const noticeLive = ` starts_at <= ? AND expires_at > ?`

// Mirror makes the store match the set the cloud listed (ADR-0176 §1.2):
// ids not in items are deleted, listed ones are inserted or refreshed, an
// edited revision resets the read/dismissed/toasted marks, expired items are
// never stored, and the bounds are enforced.
func (r *CloudNoticeRepo) Mirror(ctx context.Context, items []CloudNotice, now time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("notices mirror: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	keep := map[string]bool{}
	for _, it := range items {
		if !it.ExpiresAt.After(now) {
			continue
		}
		keep[it.ID] = true
		var oldRev int
		err := tx.QueryRowContext(ctx, `SELECT rev FROM cloud_notices WHERE id = ?`, it.ID).Scan(&oldRev)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			_, err = tx.ExecContext(ctx, `INSERT INTO cloud_notices
 (id, rev, sender_kind, sender_name, severity, lang, title, body, link_json, starts_at, expires_at, created_at, received_at)
 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				it.ID, it.Rev, it.SenderKind, it.SenderName, it.Severity, it.Lang, it.Title, it.Body, it.LinkJSON,
				noticeFmt(it.StartsAt), noticeFmt(it.ExpiresAt), noticeFmt(it.CreatedAt), noticeFmt(now))
		case err != nil:
			// fall through to the common return below
		default:
			reset := ""
			if it.Rev != oldRev {
				reset = `, read_at = NULL, dismissed_at = NULL, toasted_at = NULL, reported_at = NULL`
			}
			_, err = tx.ExecContext(ctx, `UPDATE cloud_notices SET rev=?, sender_kind=?, sender_name=?, severity=?, lang=?,
 title=?, body=?, link_json=?, starts_at=?, expires_at=?, created_at=?`+reset+` WHERE id = ?`,
				it.Rev, it.SenderKind, it.SenderName, it.Severity, it.Lang, it.Title, it.Body, it.LinkJSON,
				noticeFmt(it.StartsAt), noticeFmt(it.ExpiresAt), noticeFmt(it.CreatedAt), it.ID)
		}
		if err != nil {
			return fmt.Errorf("notices mirror: %w", err)
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM cloud_notices`)
	if err != nil {
		return fmt.Errorf("notices mirror: %w", err)
	}
	var drop []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		if !keep[id] {
			drop = append(drop, id)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	for _, id := range drop {
		if _, err := tx.ExecContext(ctx, `DELETE FROM cloud_notices WHERE id = ?`, id); err != nil {
			return fmt.Errorf("notices mirror: %w", err)
		}
	}
	if err := r.evict(ctx, tx, now); err != nil {
		return err
	}
	return tx.Commit()
}

// evict enforces the row and byte caps: oldest read first, then oldest
// expired, then oldest unread non-critical; an unread critical goes last.
func (r *CloudNoticeRepo) evict(ctx context.Context, tx *sql.Tx, now time.Time) error {
	type cand struct {
		id      string
		size    int
		rank    int
		created string
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, length(title)+length(body)+length(link_json)+length(sender_name),
 severity, (read_at IS NOT NULL OR dismissed_at IS NOT NULL), expires_at <= ?, created_at FROM cloud_notices`, noticeFmt(now))
	if err != nil {
		return fmt.Errorf("notices evict: %w", err)
	}
	var cs []cand
	total := 0
	for rows.Next() {
		var c cand
		var sev string
		var read, expired bool
		if err := rows.Scan(&c.id, &c.size, &sev, &read, &expired, &c.created); err != nil {
			_ = rows.Close()
			return err
		}
		switch {
		case read:
			c.rank = 0
		case expired:
			c.rank = 1
		case sev != cloudnotice.Critical:
			c.rank = 2
		default:
			c.rank = 3
		}
		total += c.size
		cs = append(cs, c)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	if len(cs) <= r.maxRows && total <= r.maxBytes {
		return nil
	}
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].rank != cs[j].rank {
			return cs[i].rank < cs[j].rank
		}
		return cs[i].created < cs[j].created
	})
	for len(cs) > 0 && (len(cs) > r.maxRows || total > r.maxBytes) {
		c := cs[0]
		if _, err := tx.ExecContext(ctx, `DELETE FROM cloud_notices WHERE id = ?`, c.id); err != nil {
			return fmt.Errorf("notices evict: %w", err)
		}
		cs = cs[1:]
		total -= c.size
	}
	return nil
}

// PurgeExpired deletes rows past expires_at (ADR-0176 §3 local erasure).
func (r *CloudNoticeRepo) PurgeExpired(ctx context.Context, now time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM cloud_notices WHERE expires_at <= ?`, noticeFmt(now))
	if err != nil {
		return 0, fmt.Errorf("notices purge: %w", err)
	}
	return res.RowsAffected()
}

// Reset empties the store (the setting being turned off, a reset).
func (r *CloudNoticeRepo) Reset(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM cloud_notices`)
	return err
}

// Pairs returns the held id:rev pairs for the request's have hash.
func (r *CloudNoticeRepo) Pairs(ctx context.Context) ([]cloudnotice.Pair, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, rev FROM cloud_notices`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []cloudnotice.Pair
	for rows.Next() {
		var p cloudnotice.Pair
		if err := rows.Scan(&p.ID, &p.Rev); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *CloudNoticeRepo) query(ctx context.Context, q string, args ...any) ([]CloudNotice, error) {
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CloudNotice
	for rows.Next() {
		n, err := scanNotice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// List returns live notices for the inbox, critical first then newest first.
func (r *CloudNoticeRepo) List(ctx context.Context, now time.Time, tab NoticeTab) ([]CloudNotice, error) {
	q := `SELECT ` + noticeCols + ` FROM cloud_notices WHERE` + noticeLive
	if tab == NoticeUnread {
		q += ` AND read_at IS NULL AND dismissed_at IS NULL`
	}
	q += ` ORDER BY CASE severity WHEN 'critical' THEN 0 WHEN 'important' THEN 1 ELSE 2 END, created_at DESC, id`
	ts := noticeFmt(now)
	return r.query(ctx, q, ts, ts)
}

// Get returns one notice by id.
func (r *CloudNoticeRepo) Get(ctx context.Context, id string) (CloudNotice, bool, error) {
	n, err := scanNotice(r.db.QueryRowContext(ctx, `SELECT `+noticeCols+` FROM cloud_notices WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return n, false, nil
	}
	return n, err == nil, err
}

// UnreadCount counts unread live notices and whether any is critical.
func (r *CloudNoticeRepo) UnreadCount(ctx context.Context, now time.Time) (int, bool, error) {
	ts := noticeFmt(now)
	var n, crit int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(severity = 'critical'),0) FROM cloud_notices
 WHERE`+noticeLive+` AND read_at IS NULL AND dismissed_at IS NULL`, ts, ts).Scan(&n, &crit)
	return n, crit > 0, err
}

// Strip returns the unacknowledged critical platform notices (the sticky
// in-flow strip, ADR-0176 §6/§7); a partner critical can never strip.
func (r *CloudNoticeRepo) Strip(ctx context.Context, now time.Time) ([]CloudNotice, error) {
	ts := noticeFmt(now)
	return r.query(ctx, `SELECT `+noticeCols+` FROM cloud_notices WHERE`+noticeLive+
		` AND severity = 'critical' AND sender_kind = 'platform' AND acked_at IS NULL ORDER BY created_at DESC, id`, ts, ts)
}

// NextToast returns the next important notice not yet shown as a toast and
// not read or dismissed.
func (r *CloudNoticeRepo) NextToast(ctx context.Context, now time.Time) (CloudNotice, bool, error) {
	ts := noticeFmt(now)
	n, err := scanNotice(r.db.QueryRowContext(ctx, `SELECT `+noticeCols+` FROM cloud_notices WHERE`+noticeLive+
		` AND severity = 'important' AND toasted_at IS NULL AND read_at IS NULL AND dismissed_at IS NULL
 ORDER BY created_at DESC, id LIMIT 1`, ts, ts))
	if errors.Is(err, sql.ErrNoRows) {
		return n, false, nil
	}
	return n, err == nil, err
}

// mark sets one of four constant columns once.
func (r *CloudNoticeRepo) mark(ctx context.Context, col, id string, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE cloud_notices SET `+col+` = COALESCE(`+col+`, ?) WHERE id = ?`, noticeFmt(at), id)
	return err
}

// MarkRead sets read_at once.
func (r *CloudNoticeRepo) MarkRead(ctx context.Context, id string, at time.Time) error {
	return r.mark(ctx, "read_at", id, at)
}

// MarkToasted remembers the toast for this id+rev was shown.
func (r *CloudNoticeRepo) MarkToasted(ctx context.Context, id string, at time.Time) error {
	return r.mark(ctx, "toasted_at", id, at)
}

// Dismiss sets dismissed_at once (the notice stays in All).
func (r *CloudNoticeRepo) Dismiss(ctx context.Context, id string, at time.Time) error {
	return r.mark(ctx, "dismissed_at", id, at)
}

// Acknowledge sets acked_at on a critical notice. It does not mark it read.
func (r *CloudNoticeRepo) Acknowledge(ctx context.Context, id string, at time.Time) error {
	res, err := r.db.ExecContext(ctx, `UPDATE cloud_notices SET acked_at = COALESCE(acked_at, ?) WHERE id = ? AND severity = 'critical'`, noticeFmt(at), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("notices: only a held critical notice can be acknowledged")
	}
	return nil
}

// PendingAcks returns up to limit acks not yet reported: a row never
// reported (carries delivered_at) or one with a mark newer than its report.
func (r *CloudNoticeRepo) PendingAcks(ctx context.Context, limit int) ([]NoticeAck, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, received_at, reported_at, read_at, dismissed_at, acked_at FROM cloud_notices
 WHERE reported_at IS NULL OR COALESCE(read_at,'') > reported_at OR COALESCE(dismissed_at,'') > reported_at OR COALESCE(acked_at,'') > reported_at
 ORDER BY created_at, id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NoticeAck
	for rows.Next() {
		var a NoticeAck
		var recv string
		var rep, rd, dm, ak sql.NullString
		if err := rows.Scan(&a.ID, &recv, &rep, &rd, &dm, &ak); err != nil {
			return nil, err
		}
		if !rep.Valid {
			a.DeliveredAt = recv
		}
		a.ReadAt, a.DismissedAt, a.AckedAt = rd.String, dm.String, ak.String
		out = append(out, a)
	}
	return out, rows.Err()
}

// MarkReported stamps the acks as sent.
func (r *CloudNoticeRepo) MarkReported(ctx context.Context, ids []string, at time.Time) error {
	for _, id := range ids {
		if _, err := r.db.ExecContext(ctx, `UPDATE cloud_notices SET reported_at = ? WHERE id = ?`, noticeFmt(at), id); err != nil {
			return err
		}
	}
	return nil
}
