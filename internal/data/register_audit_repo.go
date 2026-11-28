package data

// Read-only register facts for the ghost audit (ut-docs#7098): the Registers
// page and cmd/register-audit read the same rows and classify them with the
// same pure function (internal/registeraudit). SELECT only; the CLI hands in a
// connection opened read-only (mode=ro, query_only), so nothing here can write.

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// AuditQuerier is the read side of *sql.DB / *sql.Tx / *sql.Conn.
type AuditQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// RegisterAuditRow is one register with everything the audit prints.
type RegisterAuditRow struct {
	RegisterID, Name         string
	IsActive                 bool
	LocationID, LocationName string
	IsSelf                   bool
	HasLink                  bool
	DeviceID                 string // "" = unknown
	LinkSource               string
	SupersededBy             string
	RetiredReason            string
	RetiredAt                time.Time
	TillID, TillName         string // TillID from the link, else the enrolment audit row
	TillRole                 string
	TillExists               bool // a tills row with TillID exists
	TillEnrolledAt           time.Time
	TillLastSeen             time.Time
	SalesCount               int
	LastSale                 time.Time
	UnsentSales              int
	OpenShift                bool
}

// HasRegisterDevices reports whether the register_devices table exists (a DB
// older than migration 130 has none; the audit then degrades to the
// enrolment audit row).
func HasRegisterDevices(ctx context.Context, q AuditQuerier) (bool, error) {
	var n int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'register_devices'`).Scan(&n); err != nil {
		return false, fmt.Errorf("check register_devices: %w", err)
	}
	return n > 0, nil
}

const registerAuditSQL = `
WITH base AS (
  SELECT r.id AS rid, r.name AS rname, r.is_active AS ractive,
         COALESCE(r.location_id, '') AS loc_id, COALESCE(l.name, '') AS loc_name,
         __LINK_COLS__
         COALESCE(NULLIF(__LINK_TILL__, ''),
                  (SELECT a.entity_id FROM audit_log a
                    WHERE a.entity_type = 'till' AND a.action = 'till_enrolled'
                      AND CASE WHEN json_valid(a.data_json) THEN json_extract(a.data_json, '$.register_id') END = r.id
                    ORDER BY a.created_at DESC, a.rowid DESC LIMIT 1), '') AS till_id
  FROM registers r
  LEFT JOIN stock_locations l ON l.id = r.location_id
  __LINK_JOIN__
)
SELECT b.rid, b.rname, b.ractive, b.loc_id, b.loc_name,
       b.has_link, b.device_id, b.link_source, b.superseded_by, b.retired_reason, b.retired_at, b.till_id,
       COALESCE(t.id, ''), COALESCE(t.name, ''), COALESCE(t.role, ''), COALESCE(t.enrolled_at, ''), COALESCE(t.last_seen_at, ''),
       (SELECT COUNT(*) FROM sales s WHERE s.register_id = b.rid),
       COALESCE((SELECT MAX(s.created_at) FROM sales s WHERE s.register_id = b.rid), ''),
       (SELECT COUNT(*) FROM sales s WHERE s.register_id = b.rid AND s.sync_status NOT IN ('synced', 'sent')),
       (SELECT COUNT(*) FROM shifts sh WHERE sh.register_id = b.rid AND sh.closed_at IS NULL)
FROM base b
LEFT JOIN tills t ON t.id = b.till_id AND b.till_id <> ''
ORDER BY b.rname, b.rid`

// AuditRegisters returns every register (active and retired) with its link,
// till, sales and shift facts. ownRegisterID marks the self register.
func AuditRegisters(ctx context.Context, q AuditQuerier) ([]RegisterAuditRow, error) {
	hasLink, err := HasRegisterDevices(ctx, q)
	if err != nil {
		return nil, err
	}
	query := registerAuditSQL
	if hasLink {
		query = strings.NewReplacer(
			"__LINK_COLS__", `CASE WHEN rd.register_id IS NULL THEN 0 ELSE 1 END AS has_link, COALESCE(rd.device_id, '') AS device_id,
         COALESCE(rd.source, '') AS link_source, COALESCE(rd.superseded_by, '') AS superseded_by,
         COALESCE(rd.retired_reason, '') AS retired_reason, COALESCE(rd.retired_at, '') AS retired_at,`,
			"__LINK_TILL__", "rd.till_id",
			"__LINK_JOIN__", "LEFT JOIN register_devices rd ON rd.register_id = r.id",
		).Replace(query)
	} else {
		query = strings.NewReplacer(
			"__LINK_COLS__", `0 AS has_link, '' AS device_id, '' AS link_source, '' AS superseded_by, '' AS retired_reason, '' AS retired_at,`,
			"__LINK_TILL__", "''",
			"__LINK_JOIN__", "",
		).Replace(query)
	}
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("audit registers: %w", err)
	}
	defer rows.Close()
	var out []RegisterAuditRow
	for rows.Next() {
		var r RegisterAuditRow
		var active, link, openShift int
		var retiredAt, enrolledAt, lastSeen, lastSale, tillRowID string
		if err := rows.Scan(&r.RegisterID, &r.Name, &active, &r.LocationID, &r.LocationName,
			&link, &r.DeviceID, &r.LinkSource, &r.SupersededBy, &r.RetiredReason, &retiredAt, &r.TillID,
			&tillRowID, &r.TillName, &r.TillRole, &enrolledAt, &lastSeen,
			&r.SalesCount, &lastSale, &r.UnsentSales, &openShift); err != nil {
			return nil, fmt.Errorf("scan audit register: %w", err)
		}
		r.IsActive = active == 1
		r.HasLink = link == 1
		r.TillExists = tillRowID != ""
		r.OpenShift = openShift > 0
		r.RetiredAt = parseDBTime(retiredAt)
		r.TillEnrolledAt = parseDBTime(enrolledAt)
		r.TillLastSeen = parseDBTime(lastSeen)
		r.LastSale = parseDBTime(lastSale)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audit registers: %w", err)
	}
	return out, nil
}

// AuditOwnRegisterID is the self register id as the audit sees it: the
// persisted sync.till_register_id, else "reg-default" (EnsureRegister's seed).
func AuditOwnRegisterID(ctx context.Context, q AuditQuerier) string {
	var v string
	if err := q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'sync.till_register_id'`).Scan(&v); err == nil && strings.TrimSpace(v) != "" {
		return v
	}
	return "reg-default"
}

// TillAuditRow is an enrolled till as the audit's three-list comparison needs it.
type TillAuditRow struct {
	ID, Name, Role, DeviceID string
}

// AuditTills lists the enrolled tills (never their bearer hashes).
func AuditTills(ctx context.Context, q AuditQuerier) ([]TillAuditRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, name, COALESCE(role, ''), COALESCE(device_id, '') FROM tills ORDER BY name, id`)
	if err != nil {
		return nil, fmt.Errorf("audit tills: %w", err)
	}
	defer rows.Close()
	var out []TillAuditRow
	for rows.Next() {
		var t TillAuditRow
		if err := rows.Scan(&t.ID, &t.Name, &t.Role, &t.DeviceID); err != nil {
			return nil, fmt.Errorf("scan audit till: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
