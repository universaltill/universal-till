package data

// Kitchen station routing (universaltill/ut-docs#516): named prep stations
// ("Grill", "Bar") each with their own printer and/or kitchen screen
// (ut-docs#544), plus the routing rules that decide which station(s) a
// sold item's kitchen ticket line — or on-screen order — goes to.
//
// Precedence lives in ONE place — ResolveKitchenStations — so no routing
// logic leaks into internal/pages: item_station_routes rows OVERRIDE
// category_station_routes (no union); an item with neither resolves to an
// empty slice and the caller falls back to the legacy default kitchen
// printer. Stations are soft-disabled (enabled=0), never deleted, mirroring
// stock locations — so routing rows never orphan.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Kitchen station destination types (kitchen_stations.destination_type, a
// CHECK-constrained vocabulary in 001_init.sql). 'printer' prints a ticket
// (ut-docs#516); 'display' shows the station's orders on a kitchen screen
// (/kitchen-display/{id}, ut-docs#544); 'both' does both — added in #544
// because #516's review flagged that the original two-value CHECK couldn't
// represent a station that prints AND shows (the common "grill has a
// screen for the cooks and a ticket for the pass" setup).
const (
	KitchenDestinationPrinter = "printer"
	KitchenDestinationDisplay = "display"
	KitchenDestinationBoth    = "both"
)

// ValidKitchenDestinationType reports whether t is one of the three
// destination types — exact match, no trimming/case-folding: the value
// comes from a fixed <select>, so anything else is a bug or tampering.
func ValidKitchenDestinationType(t string) bool {
	switch t {
	case KitchenDestinationPrinter, KitchenDestinationDisplay, KitchenDestinationBoth:
		return true
	}
	return false
}

func validateKitchenDestinationType(t string) error {
	if !ValidKitchenDestinationType(t) {
		return fmt.Errorf("kitchen station: invalid destination type %q (want printer, display or both)", t)
	}
	return nil
}

// dbDestinationColumns decomposes the public tri-state destination type into
// the two physical columns kitchen_stations actually stores. destination_type
// stays the original two-value CHECK-constrained column (001_init.sql);
// destination_both is a plain additive flag (003_kitchen_station_display_flag.sql)
// — 'both' is stored as ('printer', 1), never as a third destination_type
// value, precisely to avoid ever needing to widen that CHECK again (see the
// migration's own comment for why that's not a safe edit on this schema).
func dbDestinationColumns(t string) (dbType string, both int) {
	if t == KitchenDestinationBoth {
		return KitchenDestinationPrinter, 1
	}
	return t, 0
}

// destinationFromColumns is dbDestinationColumns' inverse: reconstructs the
// public tri-state value every reader of this table (ListKitchenStations,
// GetKitchenStation, ResolveKitchenStations) presents to its own callers.
func destinationFromColumns(dbType string, both int) string {
	if both == 1 {
		return KitchenDestinationBoth
	}
	return dbType
}

// KitchenStation is one prep station tickets can route to. DestinationType
// is one of the KitchenDestination* constants; PrinterAddress is meaningful
// only when PrintsTickets() (a display-only station legitimately has none).
type KitchenStation struct {
	ID              string
	Name            string
	DestinationType string
	PrinterAddress  string
	Enabled         bool
	CreatedAt       string
	UpdatedAt       string
}

// PrintsTickets reports whether the station is a print destination
// ('printer' or 'both') — the kitchen_print.go filter (ut-docs#544).
func (s KitchenStation) PrintsTickets() bool {
	return s.DestinationType == KitchenDestinationPrinter || s.DestinationType == KitchenDestinationBoth
}

// ShowsOnDisplay reports whether the station has a kitchen screen
// ('display' or 'both') — gates /kitchen-display/{id} and the admin page's
// "View display" link (ut-docs#544).
func (s KitchenStation) ShowsOnDisplay() bool {
	return s.DestinationType == KitchenDestinationDisplay || s.DestinationType == KitchenDestinationBoth
}

// ItemStationOverride is one item that carries item-level routes, as the
// admin page lists it (name/SKU for display, station ids for the checkbox
// state).
type ItemStationOverride struct {
	ItemID     string
	Name       string
	SKU        string
	StationIDs []string
}

// ListKitchenStations returns every station (enabled and disabled) for the
// admin page, ordered by name.
func (r *POSRepo) ListKitchenStations(ctx context.Context) ([]KitchenStation, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, name, destination_type, destination_both, COALESCE(printer_address, ''), enabled, created_at, updated_at
FROM kitchen_stations ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list kitchen stations: %w", err)
	}
	defer rows.Close()
	var out []KitchenStation
	for rows.Next() {
		var s KitchenStation
		var enabled, both int
		if err := rows.Scan(&s.ID, &s.Name, &s.DestinationType, &both, &s.PrinterAddress, &enabled, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan kitchen station: %w", err)
		}
		s.DestinationType = destinationFromColumns(s.DestinationType, both)
		s.Enabled = enabled == 1
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate kitchen stations: %w", err)
	}
	return out, nil
}

// GetKitchenStation loads one station by id. ok=false with a nil error
// means no such station.
func (r *POSRepo) GetKitchenStation(ctx context.Context, id string) (KitchenStation, bool, error) {
	var s KitchenStation
	var enabled, both int
	err := r.db.QueryRowContext(ctx, `
SELECT id, name, destination_type, destination_both, COALESCE(printer_address, ''), enabled, created_at, updated_at
FROM kitchen_stations WHERE id = ?`, id).Scan(&s.ID, &s.Name, &s.DestinationType, &both, &s.PrinterAddress, &enabled, &s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return KitchenStation{}, false, nil
	}
	if err != nil {
		return KitchenStation{}, false, fmt.Errorf("get kitchen station: %w", err)
	}
	s.DestinationType = destinationFromColumns(s.DestinationType, both)
	s.Enabled = enabled == 1
	return s, true, nil
}

// CreateKitchenStation adds a new, enabled station of the given destination
// type (validated against the KitchenDestination* vocabulary before any
// write — the schema CHECK is the backstop, not the error message).
// Whether printerAddress is required is the caller's (form's) rule: it
// depends on the type, and the repo stays a plain persistence layer.
func (r *POSRepo) CreateKitchenStation(ctx context.Context, name, destinationType, printerAddress string) (string, error) {
	if err := validateKitchenDestinationType(destinationType); err != nil {
		return "", fmt.Errorf("create kitchen station: %w", err)
	}
	id := uuid.NewString()
	dbType, both := dbDestinationColumns(destinationType)
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := r.db.ExecContext(ctx, `
INSERT INTO kitchen_stations (id, name, destination_type, destination_both, printer_address, enabled, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, 1, ?, ?)`, id, name, dbType, both, printerAddress, now, now); err != nil {
		return "", fmt.Errorf("create kitchen station: %w", err)
	}
	return id, nil
}

// UpdateKitchenStation changes a station's display name, destination type
// and printer address; the id (and every routing row keyed by it) is
// unaffected. An invalid destination type is rejected before any write.
func (r *POSRepo) UpdateKitchenStation(ctx context.Context, id, name, destinationType, printerAddress string) error {
	if err := validateKitchenDestinationType(destinationType); err != nil {
		return fmt.Errorf("update kitchen station: %w", err)
	}
	dbType, both := dbDestinationColumns(destinationType)
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := r.db.ExecContext(ctx, `
UPDATE kitchen_stations SET name = ?, destination_type = ?, destination_both = ?, printer_address = ?, updated_at = ? WHERE id = ?`,
		name, dbType, both, printerAddress, now, id)
	if err != nil {
		return fmt.Errorf("update kitchen station: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("update kitchen station: %s not found", id)
	}
	return nil
}

// ListRecentOrdersForStation is the station-scoped twin of ListRecentOrders
// (order_status_repo.go) for a kitchen display (ut-docs#544): the same row
// shape, the same completed/non-terminal filter, the same newest-first
// order and cap — restricted to sales with at least one line whose item
// ROUTES TO stationID.
//
// "Routes to" must mean exactly what ResolveKitchenStations means, or a
// ticket and a screen would disagree about the same order. That precedence
// is re-stated here in SQL rather than by calling ResolveKitchenStations per
// order (which would be N+1 over the whole open queue on every 15s poll):
//
//   - the station must be enabled (a disabled station routes nothing —
//     ResolveKitchenStations' enabled filter);
//   - a line's item matches if it has an item_station_routes row for THIS
//     station; OR it has NO item_station_routes rows at all AND its
//     category has a category_station_routes row for this station. Item
//     rows CLAIM the tier by existence, whatever station they point at —
//     an item overridden to some other station never falls back to its
//     category rule (TestListRecentOrdersForStation_ItemRowsClaimTierEvenWhenTheyMissThisStation
//     mirrors ResolveKitchenStations' disabled-override test);
//   - a variant-only line (item_id NULL) never matches — the same
//     limitation buildKitchenTargets has, not a new one.
//
// EXISTS (not JOIN) so an order with several lines routed here is listed
// once, and the outer query stays a plain scan of `sales` with the same
// shape as ListRecentOrders. Order status is per ORDER, not per line: an
// order whose lines split across two display stations is listed on both,
// and advancing it on either clears it from both — matches every other
// board's granularity today.
func (r *POSRepo) ListRecentOrdersForStation(ctx context.Context, stationID string, limit int) ([]OrderListEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT s.receipt_no, COALESCE(s.order_type, ''), s.order_status, COALESCE(s.order_status_updated_at, ''), s.created_at,
       COALESCE(s.kitchen_print_failed_at, ''), COALESCE(s.receipt_print_failed_at, '')
FROM sales s
WHERE s.sale_type = 'sale' AND s.status = 'completed'
  AND s.order_status NOT IN ('collected', 'cancelled')
  AND EXISTS (SELECT 1 FROM kitchen_stations ks WHERE ks.id = ?1 AND ks.enabled = 1)
  AND EXISTS (
    SELECT 1
    FROM sale_lines l
    JOIN items i ON i.id = l.item_id
    WHERE l.sale_id = s.id
      AND (
        EXISTS (SELECT 1 FROM item_station_routes ir WHERE ir.item_id = i.id AND ir.station_id = ?1)
        OR (
          NOT EXISTS (SELECT 1 FROM item_station_routes ir2 WHERE ir2.item_id = i.id)
          AND EXISTS (SELECT 1 FROM category_station_routes cr WHERE cr.category_id = i.category_id AND cr.station_id = ?1)
        )
      )
  )
ORDER BY s.created_at DESC
LIMIT ?2
`, stationID, limit)
	if err != nil {
		return nil, fmt.Errorf("list recent orders for station: %w", err)
	}
	defer rows.Close()
	var out []OrderListEntry
	for rows.Next() {
		var e OrderListEntry
		if err := rows.Scan(&e.ReceiptNo, &e.OrderType, &e.Status, &e.StatusUpdatedAt, &e.CreatedAt, &e.KitchenPrintFailedAt, &e.ReceiptPrintFailedAt); err != nil {
			return nil, fmt.Errorf("scan recent order for station: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list recent orders for station: %w", err)
	}
	return out, nil
}

// SetKitchenStationEnabled soft-disables/re-enables a station, mirroring
// SetStockLocationActive — no hard delete, so routing rows never orphan.
// A disabled station simply stops receiving tickets (its lines fall back to
// the default kitchen printer via ResolveKitchenStations' enabled filter).
func (r *POSRepo) SetKitchenStationEnabled(ctx context.Context, id string, enabled bool) error {
	v := 0
	if enabled {
		v = 1
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := r.db.ExecContext(ctx, `
UPDATE kitchen_stations SET enabled = ?, updated_at = ? WHERE id = ?`, v, now, id)
	if err != nil {
		return fmt.Errorf("set kitchen station enabled: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("set kitchen station enabled: %s not found", id)
	}
	return nil
}

// SetItemStationRoutes replaces an item's station routes with exactly the
// given set, in one transaction. An empty set removes the item-level
// override entirely (the item falls back to its category's routes).
func (r *POSRepo) SetItemStationRoutes(ctx context.Context, itemID string, stationIDs []string) error {
	return r.replaceStationRoutes(ctx, "item_station_routes", "item_id", itemID, stationIDs)
}

// SetCategoryStationRoutes replaces a category's station routes with exactly
// the given set, in one transaction.
func (r *POSRepo) SetCategoryStationRoutes(ctx context.Context, categoryID string, stationIDs []string) error {
	return r.replaceStationRoutes(ctx, "category_station_routes", "category_id", categoryID, stationIDs)
}

// replaceStationRoutes is the shared replace-all (delete + insert) for both
// routing tables. table/keyCol are compile-time constants at both call
// sites, never user input.
func (r *POSRepo) replaceStationRoutes(ctx context.Context, table, keyCol, keyID string, stationIDs []string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("replace %s: begin: %w", table, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM `+table+` WHERE `+keyCol+` = ?`, keyID); err != nil {
		return fmt.Errorf("replace %s: delete: %w", table, err)
	}
	seen := map[string]bool{}
	for _, sid := range stationIDs {
		sid = strings.TrimSpace(sid)
		if sid == "" || seen[sid] {
			continue
		}
		seen[sid] = true
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO `+table+` (`+keyCol+`, station_id) VALUES (?, ?)`, keyID, sid); err != nil {
			return fmt.Errorf("replace %s: insert: %w", table, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("replace %s: commit: %w", table, err)
	}
	return nil
}

// ItemStationRoutes returns the station ids an item is explicitly routed to
// (raw rows, no enabled filter — the admin page shows the configured state).
func (r *POSRepo) ItemStationRoutes(ctx context.Context, itemID string) ([]string, error) {
	return r.stationRouteIDs(ctx, `SELECT station_id FROM item_station_routes WHERE item_id = ?`, itemID)
}

// CategoryStationRoutes returns the station ids a category is routed to
// (raw rows, no enabled filter — the admin page shows the configured state).
func (r *POSRepo) CategoryStationRoutes(ctx context.Context, categoryID string) ([]string, error) {
	return r.stationRouteIDs(ctx, `SELECT station_id FROM category_station_routes WHERE category_id = ?`, categoryID)
}

func (r *POSRepo) stationRouteIDs(ctx context.Context, query, keyID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, query, keyID)
	if err != nil {
		return nil, fmt.Errorf("station routes: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan station route: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate station routes: %w", err)
	}
	return out, nil
}

// AllCategoryStationRoutes returns every category's configured station ids
// in one query, for rendering the admin page's category × station matrix.
func (r *POSRepo) AllCategoryStationRoutes(ctx context.Context) (map[string][]string, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT category_id, station_id FROM category_station_routes ORDER BY category_id`)
	if err != nil {
		return nil, fmt.Errorf("all category station routes: %w", err)
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var catID, stationID string
		if err := rows.Scan(&catID, &stationID); err != nil {
			return nil, fmt.Errorf("scan category station route: %w", err)
		}
		out[catID] = append(out[catID], stationID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate category station routes: %w", err)
	}
	return out, nil
}

// ListItemStationOverrides returns every item that carries item-level routes,
// with its display name/SKU and configured station ids, for the admin page's
// overrides list. (Items are too numerous to list wholesale — category
// routing is the primary mechanism; this lists only the exceptions.)
func (r *POSRepo) ListItemStationOverrides(ctx context.Context) ([]ItemStationOverride, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT i.id, i.name, COALESCE(i.sku, ''), r.station_id
FROM item_station_routes r
JOIN items i ON i.id = r.item_id
ORDER BY i.name, i.id`)
	if err != nil {
		return nil, fmt.Errorf("list item station overrides: %w", err)
	}
	defer rows.Close()
	var out []ItemStationOverride
	index := map[string]int{}
	for rows.Next() {
		var itemID, name, sku, stationID string
		if err := rows.Scan(&itemID, &name, &sku, &stationID); err != nil {
			return nil, fmt.Errorf("scan item station override: %w", err)
		}
		i, ok := index[itemID]
		if !ok {
			i = len(out)
			index[itemID] = i
			out = append(out, ItemStationOverride{ItemID: itemID, Name: name, SKU: sku})
		}
		out[i].StationIDs = append(out[i].StationIDs, stationID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate item station overrides: %w", err)
	}
	return out, nil
}

// ResolveKitchenStations is THE routing algorithm (ut-docs#516) — the single
// SQL-backed source of truth callers group tickets by. For each item id:
//
//   - item_station_routes rows present → exactly those stations (item-level
//     OVERRIDES category-level, no union) — then filtered to enabled=1;
//   - otherwise, its category's category_station_routes (enabled=1 only);
//   - otherwise absent from the map (unrouted → the caller falls back to the
//     legacy default kitchen printer).
//
// The tier is decided by row EXISTENCE, then the enabled filter applies: an
// item whose only routed station is disabled resolves empty (safe default
// printer), it does not resurrect the category rule the operator overrode.
func (r *POSRepo) ResolveKitchenStations(ctx context.Context, itemIDs []string) (map[string][]KitchenStation, error) {
	out := map[string][]KitchenStation{}
	ids := make([]string, 0, len(itemIDs))
	seen := map[string]bool{}
	for _, id := range itemIDs {
		if id = strings.TrimSpace(id); id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return out, nil
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}

	// Item tier: every item-level row (including disabled stations, so row
	// existence still claims the tier), enabled flag carried for filtering.
	hasItemRows := map[string]bool{}
	itemRows, err := r.db.QueryContext(ctx, `
SELECT r.item_id, s.id, s.name, s.destination_type, s.destination_both, COALESCE(s.printer_address, ''), s.enabled
FROM item_station_routes r
JOIN kitchen_stations s ON s.id = r.station_id
WHERE r.item_id IN (`+placeholders+`)
ORDER BY s.name`, args...)
	if err != nil {
		return nil, fmt.Errorf("resolve kitchen stations (items): %w", err)
	}
	if err := collectStationRows(itemRows, out, hasItemRows); err != nil {
		return nil, err
	}

	// Category tier: only for items with NO item-level rows.
	catRows, err := r.db.QueryContext(ctx, `
SELECT i.id, s.id, s.name, s.destination_type, s.destination_both, COALESCE(s.printer_address, ''), s.enabled
FROM items i
JOIN category_station_routes r ON r.category_id = i.category_id
JOIN kitchen_stations s ON s.id = r.station_id
WHERE i.id IN (`+placeholders+`) AND s.enabled = 1
ORDER BY s.name`, args...)
	if err != nil {
		return nil, fmt.Errorf("resolve kitchen stations (categories): %w", err)
	}
	catResolved := map[string][]KitchenStation{}
	if err := collectStationRows(catRows, catResolved, map[string]bool{}); err != nil {
		return nil, err
	}
	for itemID, stations := range catResolved {
		if !hasItemRows[itemID] {
			out[itemID] = stations
		}
	}
	// Items whose item-level rows all pointed at disabled stations keep an
	// explicit empty entry only if the filter dropped everything — normalise
	// those away so "unrouted" is uniformly a missing/empty key.
	for itemID, stations := range out {
		if len(stations) == 0 {
			delete(out, itemID)
		}
	}
	return out, nil
}

// collectStationRows scans (item_id, station...) rows into dst, recording
// row existence per item in hasRows and dropping disabled stations.
func collectStationRows(rows *sql.Rows, dst map[string][]KitchenStation, hasRows map[string]bool) error {
	defer rows.Close()
	for rows.Next() {
		var itemID string
		var s KitchenStation
		var enabled, both int
		if err := rows.Scan(&itemID, &s.ID, &s.Name, &s.DestinationType, &both, &s.PrinterAddress, &enabled); err != nil {
			return fmt.Errorf("scan resolved kitchen station: %w", err)
		}
		s.DestinationType = destinationFromColumns(s.DestinationType, both)
		hasRows[itemID] = true
		if _, ok := dst[itemID]; !ok {
			dst[itemID] = nil
		}
		if enabled != 1 {
			continue
		}
		s.Enabled = true
		dst[itemID] = append(dst[itemID], s)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate resolved kitchen stations: %w", err)
	}
	return nil
}
