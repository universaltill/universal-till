package data

// Kitchen station routing (universaltill/ut-docs#516): named prep stations
// ("Grill", "Bar") each with their own printer, plus the routing rules that
// decide which station(s) a sold item's kitchen ticket line goes to.
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
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// KitchenStation is one prep station tickets can route to. DestinationType
// is 'printer' for everything this slice creates; 'display' is admitted by
// the schema for the Kitchen Display slice (ut-docs#544) but has no UI yet.
type KitchenStation struct {
	ID              string
	Name            string
	DestinationType string
	PrinterAddress  string
	Enabled         bool
	CreatedAt       string
	UpdatedAt       string
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
SELECT id, name, destination_type, COALESCE(printer_address, ''), enabled, created_at, updated_at
FROM kitchen_stations ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list kitchen stations: %w", err)
	}
	defer rows.Close()
	var out []KitchenStation
	for rows.Next() {
		var s KitchenStation
		var enabled int
		if err := rows.Scan(&s.ID, &s.Name, &s.DestinationType, &s.PrinterAddress, &enabled, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan kitchen station: %w", err)
		}
		s.Enabled = enabled == 1
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate kitchen stations: %w", err)
	}
	return out, nil
}

// CreateKitchenStation adds a new, enabled 'printer' station. This slice
// only creates printer stations — the 'display' destination is ut-docs#544.
func (r *POSRepo) CreateKitchenStation(ctx context.Context, name, printerAddress string) (string, error) {
	id := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := r.db.ExecContext(ctx, `
INSERT INTO kitchen_stations (id, name, destination_type, printer_address, enabled, created_at, updated_at)
VALUES (?, ?, 'printer', ?, 1, ?, ?)`, id, name, printerAddress, now, now); err != nil {
		return "", fmt.Errorf("create kitchen station: %w", err)
	}
	return id, nil
}

// UpdateKitchenStation changes a station's display name and printer address;
// the id (and every routing row keyed by it) is unaffected.
func (r *POSRepo) UpdateKitchenStation(ctx context.Context, id, name, printerAddress string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := r.db.ExecContext(ctx, `
UPDATE kitchen_stations SET name = ?, printer_address = ?, updated_at = ? WHERE id = ?`,
		name, printerAddress, now, id)
	if err != nil {
		return fmt.Errorf("update kitchen station: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("update kitchen station: %s not found", id)
	}
	return nil
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
SELECT r.item_id, s.id, s.name, s.destination_type, COALESCE(s.printer_address, ''), s.enabled
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
SELECT i.id, s.id, s.name, s.destination_type, COALESCE(s.printer_address, ''), s.enabled
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
		var enabled int
		if err := rows.Scan(&itemID, &s.ID, &s.Name, &s.DestinationType, &s.PrinterAddress, &enabled); err != nil {
			return fmt.Errorf("scan resolved kitchen station: %w", err)
		}
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
