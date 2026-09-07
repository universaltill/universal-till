package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Cross-till table occupancy, replica side (ut-docs#1392): the floor-plan
// tiles (tables_page.go) and the basket's table picker (table_picker_api.go)
// both call tablesWithStateForDisplay instead of repo.ListTablesWithState
// directly, so a replica DISPLAYS the primary's live occupancy when
// reachable, MERGED with its own local occupancy, falling back — silently —
// to local-only on ANY failure, same fallback shape as fetchOrdersFromPrimary
// (order_status.go) for the Orders board. This is READ-ONLY: it changes what
// a replica shows, never how a claim is taken, held or released —
// IsTableFree/ClaimTable/ReleaseTableClaim are untouched, still purely
// local. See registerSyncTables's own doc comment for why write-through
// claim enforcement is a separate, harder change (ut-docs#1703), not this
// one.
//
// MERGE, never REPLACE (round-2 review finding, 2026-09-07): table_claims
// and held_sales are both local-only — sync_admin_repo.go's own adminTables
// doc comment excludes them by name — so the primary has never seen and
// never reports THIS till's own live claim or parked held order. Simply
// returning the primary's rows verbatim (the first draft's bug) made a
// satellite's own occupied tables read as free on its own floor plan and
// picker the moment the primary was reachable — a regression worse than the
// display gap this card exists to close. tablesWithStateForDisplay ORs each
// table's Occupied flag from both sources instead.
//
// Timeout is SHORTER than fetchOrdersFromPrimary's 3s (round-2 review
// finding): that precedent's budget was chosen for a 15s background poll
// (order_status.go), but this call sits on a much hotter path —
// web/ui/partials/basket.html's table-picker span is `hx-trigger="load"`,
// so it fires on every basket render (every add/qty change), not once per
// 15s. A blackholed (not merely refused) primary at 3s would stall every
// single basket edit for 3 full seconds; checkout itself still completes
// either way (offline-first unchanged), but a shorter budget keeps a dead
// primary from making the whole till feel frozen on the hot path.
var tablesProxyClient = &http.Client{Timeout: 800 * time.Millisecond}

// syncTableRow is the wire form of one data.TableWithState — the shape GET
// /api/sync/tables returns and a replica decodes back into the same type
// its own tiles()/table-picker code already renders.
type syncTableRow struct {
	ID            string `json:"id"`
	Label         string `json:"label"`
	AreaZone      string `json:"area_zone"`
	SeatCount     int    `json:"seat_count"`
	Shape         string `json:"shape"`
	PosX          int    `json:"pos_x"`
	PosY          int    `json:"pos_y"`
	Enabled       bool   `json:"enabled"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
	Occupied      bool   `json:"occupied"`
	OccupiedSince string `json:"occupied_since"`
}

// tablesWithStateForDisplay is the one call every table-occupancy DISPLAY
// site should use instead of repo.ListTablesWithState directly: it always
// starts from this till's own local state (the only source that knows
// about THIS till's own live claims/held orders — see the file-level
// comment on why that can never be dropped), then, on a replica with a
// reachable primary, ORs in the primary's occupancy per table — so a table
// occupied EITHER here or on the primary shows occupied. Metadata (label,
// position, ...) always comes from the local row; only Occupied/
// OccupiedSince are ever enriched from the primary. Any failure reaching
// the primary (not a replica, network error, non-200, malformed body)
// leaves the local view untouched — offline-first, unchanged.
func tablesWithStateForDisplay(ctx context.Context, d *common.Deps, repo *data.POSRepo) ([]data.TableWithState, error) {
	local, err := repo.ListTablesWithState(ctx)
	if err != nil {
		return nil, err
	}
	primaryRows, ok := fetchTablesFromPrimary(ctx, d, tablesProxyClient)
	if !ok {
		return local, nil
	}
	primaryByID := make(map[string]data.TableWithState, len(primaryRows))
	for _, p := range primaryRows {
		primaryByID[p.ID] = p
	}
	merged := make([]data.TableWithState, len(local))
	for i, l := range local {
		merged[i] = l
		p, found := primaryByID[l.ID]
		if !found || !p.Occupied {
			continue
		}
		merged[i].Occupied = true
		merged[i].OccupiedSince = earliestOccupiedSince(l.OccupiedSince, p.OccupiedSince)
	}
	return merged, nil
}

// earliestOccupiedSince picks the earlier non-empty OccupiedSince between
// this till's own local occupancy source and the primary's — same
// "compare as raw text, exact within one shape, only approximate across
// the two" convention ListTablesWithState's own SQL UNION+MIN already
// accepts for this column (see its doc comment in tables_repo.go).
func earliestOccupiedSince(local, primary string) string {
	if local == "" {
		return primary
	}
	if primary == "" || local <= primary {
		return local
	}
	return primary
}

// fetchTablesFromPrimary tries GET /api/sync/tables on the primary. ok=false
// on ANY failure — not a replica, network error, timeout, non-200,
// malformed body — and the caller falls through to the local list; the
// fallback is silent to the operator by design (Debugf only), same
// convention as fetchOrdersFromPrimary.
func fetchTablesFromPrimary(ctx context.Context, d *common.Deps, client *http.Client) ([]data.TableWithState, bool) {
	base, bearer, isReplica := replicaSyncTarget(ctx, d)
	if !isReplica {
		return nil, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/sync/tables", nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := client.Do(req)
	if err != nil {
		logging.L().Debugf("tables proxy: primary unreachable (%v) — using local list", err)
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		logging.L().Debugf("tables proxy: primary answered %s — using local list", resp.Status)
		return nil, false
	}
	var out struct {
		Data []syncTableRow `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		logging.L().Debugf("tables proxy: malformed primary response (%v) — using local list", err)
		return nil, false
	}
	rows := make([]data.TableWithState, 0, len(out.Data))
	for _, row := range out.Data {
		rows = append(rows, data.TableWithState{
			Table: data.Table{
				ID:        row.ID,
				Label:     row.Label,
				AreaZone:  row.AreaZone,
				SeatCount: row.SeatCount,
				Shape:     row.Shape,
				PosX:      row.PosX,
				PosY:      row.PosY,
				Enabled:   row.Enabled,
				CreatedAt: row.CreatedAt,
				UpdatedAt: row.UpdatedAt,
			},
			Occupied:      row.Occupied,
			OccupiedSince: row.OccupiedSince,
		})
	}
	return rows, true
}
