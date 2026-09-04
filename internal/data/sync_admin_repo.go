package data

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/universaltill/universal-till/internal/logging"
)

// SyncAdminRepo serves LAN sync increment D2b (ADR-0011): the primary dumps
// its admin-managed state (catalog, users, settings, translations) as one
// bundle; a replica applies it wholesale — primary wins. A whole-bundle
// fingerprint replaces per-table cursors so deletes propagate too.
type SyncAdminRepo struct{ db *sql.DB }

func NewSyncAdminRepo(db *sql.DB) *SyncAdminRepo { return &SyncAdminRepo{db: db} }

// adminTable describes one synced table. Order is FK-safe for inserts;
// deletes run in reverse. All synced PKs are TEXT.
type adminTable struct {
	name        string
	pk          []string
	hasIsActive bool   // fallback when a hard delete is FK-blocked (sales history)
	activeCol   string // overrides the retire-in-place column name when the
	// table's own soft-delete flag isn't literally named "is_active" (e.g.
	// tables.enabled). Only meaningful together with hasIsActive.
	unique []string // non-PK UNIQUE columns, mangled on that fallback: a
	// kept-but-retired row must release values like sku/username, or the
	// primary's row upserts into a UNIQUE violation and the whole apply
	// rolls back — on every pull, forever.
	skipCols []string // till-LOCAL columns that must never travel: excluded
	// from the dump and ignored on apply even if an older primary sends them
	// — an existing local value (e.g. payment_methods.plugin_id) is left
	// exactly as-is, never overwritten either way.
	redactCols []string // SECRET columns that must never travel, stronger
	// than skipCols: excluded from the dump like skipCols, but also forced
	// to NULL on every apply, on every row, even one that already existed
	// locally. Required for anything a replica could otherwise end up
	// holding through a path OTHER than this table's own incremental sync
	// (ut-docs#405: a replica enrolled via a full-DB-snapshot join already
	// has the primary's real tills rows, bearer_hash included, baked in
	// from day one — skipCols's "leave it alone" would let that real
	// secret sit there forever; redactCols actively scrubs it every pull).
}

// adminTables is the shop-wide state a replica mirrors. Deliberately NOT
// here: inventory/stock (additive movements, D3), sales, sessions,
// stock_locations (per-till), item_images (files don't travel — D2 limit),
// plugin install tables — rows without the Ed25519-verified plugin FILES
// would leave a replica with phantom plugins, so the installed set travels
// as its own registry bundle instead (GET /api/sync/plugins, ut-docs#460 /
// ADR-0011 amendment 2026-08-08: SyncPluginsRepo) and each replica
// re-fetches + re-verifies every listing from the marketplace itself.
// plugin_settings IS here, but only its GLOBAL-scope rows travel (shop-wide
// config like a payment gateway's secret key); register/user-scoped rows
// stay per-till. See applyPluginSettings for its special apply semantics.
var adminTables = []adminTable{
	{name: "tax_codes", pk: []string{"id"}, hasIsActive: true, unique: []string{"name"}},
	{name: "brands", pk: []string{"id"}, unique: []string{"name"}},
	{name: "categories", pk: []string{"id"}},
	{name: "customers", pk: []string{"id"}, unique: []string{"loyalty_no"}},
	// plugin_id is till-local derived state (which plugin installed on THIS
	// till owns the method) — importing it re-hijacks a repaired built-in
	// from a not-yet-upgraded primary (ADR-0031).
	{name: "payment_methods", pk: []string{"id"}, hasIsActive: true, unique: []string{"name"}, skipCols: []string{"plugin_id"}},
	{name: "users", pk: []string{"id"}, hasIsActive: true, unique: []string{"username"}},
	{name: "items", pk: []string{"id"}, hasIsActive: true, unique: []string{"sku"}},
	{name: "item_barcodes", pk: []string{"barcode"}},
	{name: "item_variants", pk: []string{"id"}, hasIsActive: true, unique: []string{"sku"}},
	{name: "variant_barcodes", pk: []string{"barcode"}},
	{name: "related_items", pk: []string{"item_id", "related_item_id"}},
	{name: "promotions", pk: []string{"code"}, hasIsActive: true},
	{name: "shortcut_buttons", pk: []string{"barcode"}},
	// ut-docs#1546: the floor plan and kitchen routing are shop-wide setup,
	// not per-till state, and were missing from this list entirely. Reported
	// from the pilot pair on 2026-09-04 — "I added a table in the main till
	// but it didn't sync with the secondary" — and it is worse than one
	// table: a satellite could not see the floor plan at all, so it could
	// neither take nor settle a table order, and kitchen tickets routed on
	// the primary went nowhere from the satellite. Ordered after items and
	// categories because the two route tables carry FKs onto both.
	// A hard delete is FK-blocked once a table has ever been referenced by a
	// sale/held-sale — the app itself never hard-deletes a table (it always
	// soft-disables via SetTableEnabled, tables.enabled), but the sync
	// fallback needs to mirror that convention too, or a hard-deleted-but-
	// used row would silently stay a permanent ghost on the satellite (found
	// in review, ut-docs#1546): retire in place via the table's own
	// `enabled` column, same shape as the is_active fallback above.
	{name: "tables", pk: []string{"id"}, hasIsActive: true, activeCol: "enabled"},
	// printer_address is till-LOCAL and must not travel (ut-docs#1546 review,
	// caught independently by a second concurrent cycle sweeping this same
	// PR). The field accepts a network address OR a device path (see
	// help/*/kitchen-stations.md), and a device path is local by
	// construction: a satellite inheriting the primary's "/dev/usb/lp0" would
	// print the primary's kitchen tickets to whatever happens to be plugged
	// into its own first USB port, or nowhere. With the column skipped, a
	// routed line falls back to the satellite's own default kitchen printer
	// (kitchen_print.go). Same shape as payment_methods' skipCols above.
	{name: "kitchen_stations", pk: []string{"id"}, skipCols: []string{"printer_address"}},
	{name: "item_station_routes", pk: []string{"item_id", "station_id"}},
	{name: "category_station_routes", pk: []string{"category_id", "station_id"}},
	{name: "translation_overrides", pk: []string{"locale", "key"}},
	{name: "settings", pk: []string{"key"}},
	// pk is the surrogate uuid, not (plugin_id,key,scope,scope_id): that
	// table-level UNIQUE constraint includes scope_id, which is NULL on
	// global rows, and SQLite treats NULLs as distinct -- ON CONFLICT on
	// it would never fire. ux_plugin_settings_global (migration 053,
	// ut-docs#787) IS a real, targetable unique constraint for global
	// rows ((plugin_id, key) WHERE scope='global') -- applyPluginSettings
	// still doesn't upsert against it, relying on its own per-plugin
	// delete-then-insert instead (see applyPluginSettings' own comment).
	{name: "plugin_settings", pk: []string{"id"}},
	// The shop's till roster (ut-docs#405) — so a replica's sync chip / the
	// /tills page has something real to show instead of an always-empty
	// local table (this table used to be primary-only: only InsertTill,
	// called from the enrolment handler on the primary, ever wrote to it).
	// Both redacted columns share the same reason redactCols exists at all
	// (see that field's comment): a replica can already hold a REAL value
	// for either one through a path other than this table's own sync (the
	// enrolment snapshot is a full-DB copy, ut-docs#368) — skipCols's
	// "leave whatever's there alone" would let a stale-or-secret value sit
	// forever; redactCols actively clears it on every single apply.
	//   - bearer_hash: EVERY till's sync-auth secret, must never leave the
	//     primary at all. See migration 030 for why the column had to
	//     become nullable to support force-NULLing it.
	//   - last_seen_at: not secret, just would-be-stale — a snapshot-joined
	//     replica starts with the primary's real timestamps baked in, and
	//     they'd never update again (nothing but the primary ever writes
	//     this column). Redacting it means a replica honestly shows "—"
	//     for a sibling's last-seen time instead of a timestamp that can
	//     be arbitrarily old. It's also NEVER put in skipCols instead:
	//     TillByBearerHash touches it on every single authenticated sync
	//     call, so including it in the dump at all would move the whole
	//     bundle's fingerprint on every pull, permanently defeating the
	//     ?have= unchanged-poll check for the ENTIRE bundle, not just this
	//     table — pinned by TestAdminDumpFingerprint_StableAcrossTillAuthTouch.
	{name: "tills", pk: []string{"id"}, redactCols: []string{"bearer_hash", "last_seen_at"}},
}

// FiscalPendingSignRetriesSettingsKey is the settings.key the pre-1.4.0
// fiscal-signing retry queue lived under. Duplicated from
// internal/pages/common.KeyPendingFiscalSignRetries ("fiscal.pending_sign_retries")
// rather than imported: internal/pages/common already imports internal/data
// (see internal/pages/common/barcode_conflict.go), so the reverse import
// would cycle — same constraint as data.StoreCountrySettingsKey just above
// in this package's sibling reset_archive_repo.go. Exported (rather than a
// private literal re-typed in two places) so
// TestFiscalPendingSignRetriesSettingsKeyMatchesCommon (in
// internal/pages/common) can assert the two never drift apart instead of
// each just trusting the other's copy.
const FiscalPendingSignRetriesSettingsKey = "fiscal.pending_sign_retries"

// PerTillSettingPrefixes are settings that belong to ONE till, never synced:
// the replica's own sync identity/cursors, its printer, its screen, its own
// end-of-day schedule (a replica Z-report would only cover local data), and
// its own fiscal-signing retry bookkeeping (ut-docs#844: tender-time state,
// never something that should sync between tills in the first place — and
// its exclusion here is what actually enforces
// pages.dropStaleFiscalSignRetryQueue's "must not linger" claim, since that
// migration only runs once at boot and can't otherwise stop a pre-1.4.0
// primary re-seeding it onto an already-migrated replica on a later sync).
// FiscalPendingSignRetriesSettingsKey is a full key, not a prefix family
// like the other four entries — strings.HasPrefix on an equal-length string
// is a true equality test, so this works as an exact match.
var PerTillSettingPrefixes = []string{"sync.", "printer.", "display.", "reports.eod_", FiscalPendingSignRetriesSettingsKey}

func perTillSetting(key string) bool {
	for _, p := range PerTillSettingPrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

// AdminBundle is the wire payload of GET /api/sync/admin.
type AdminBundle struct {
	Tables map[string][]map[string]any `json:"tables"`
}

// Fingerprint identifies the bundle content: rows are ordered by PK and
// json.Marshal sorts map keys, so equal state hashes equal.
func (b AdminBundle) Fingerprint() string {
	raw, _ := json.Marshal(b.Tables)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:16])
}

// DumpAdmin reads every synced table (per-till settings filtered out).
func (r *SyncAdminRepo) DumpAdmin(ctx context.Context) (AdminBundle, error) {
	bundle := AdminBundle{Tables: map[string][]map[string]any{}}
	for _, t := range adminTables {
		rows, err := r.db.QueryContext(ctx,
			`SELECT * FROM `+t.name+` ORDER BY `+strings.Join(t.pk, ", "))
		if err != nil {
			return AdminBundle{}, fmt.Errorf("dump %s: %w", t.name, err)
		}
		recs, err := scanGeneric(rows)
		rows.Close()
		if err != nil {
			return AdminBundle{}, fmt.Errorf("dump %s: %w", t.name, err)
		}
		if t.name == "settings" {
			kept := recs[:0]
			for _, rec := range recs {
				if !perTillSetting(fmt.Sprint(rec["key"])) {
					kept = append(kept, rec)
				}
			}
			recs = kept
		}
		if t.name == "plugin_settings" {
			kept := recs[:0]
			for _, rec := range recs {
				if fmt.Sprint(rec["scope"]) == "global" {
					kept = append(kept, rec)
				}
			}
			recs = kept
		}
		for _, c := range t.skipCols {
			for _, rec := range recs {
				delete(rec, c)
			}
		}
		for _, c := range t.redactCols {
			for _, rec := range recs {
				delete(rec, c)
			}
		}
		bundle.Tables[t.name] = recs
	}
	return bundle, nil
}

// ApplyAdmin makes this till's admin state match the bundle, in one
// transaction: first delete rows the primary no longer has (children
// first; sales-referenced rows fall back to is_active=0), then upsert
// everything. Tables absent from the bundle are left untouched.
func (r *SyncAdminRepo) ApplyAdmin(ctx context.Context, bundle AdminBundle) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("apply admin: %w", err)
	}
	defer tx.Rollback()

	// Phase 1 — deletes, children first, so UNIQUE collisions (e.g. a
	// replica-local item holding a SKU the primary now uses) clear before
	// the upserts land.
	for i := len(adminTables) - 1; i >= 0; i-- {
		t := adminTables[i]
		recs, ok := bundle.Tables[t.name]
		if !ok {
			continue
		}
		// settings are never pruned: the app only ever upserts them, and a
		// key a newer replica writes that the primary doesn't know must not
		// be wiped on every pull (version skew). plugin_settings does its own
		// scoped replace in applyPluginSettings — the generic prune would
		// wipe this till's register/user-scoped rows (absent from the bundle).
		if t.name == "settings" || t.name == "plugin_settings" {
			continue
		}
		if err := deleteMissing(ctx, tx, t, recs); err != nil {
			return err
		}
	}

	// Phase 2 — upserts in FK order.
	for _, t := range adminTables {
		recs, ok := bundle.Tables[t.name]
		if !ok {
			continue
		}
		if t.name == "plugin_settings" {
			if err := applyPluginSettings(ctx, tx, t, recs); err != nil {
				return err
			}
			continue
		}
		cols, err := tableColumns(ctx, tx, t.name)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			if t.name == "settings" && perTillSetting(fmt.Sprint(rec["key"])) {
				continue // defense in depth: never let a primary write per-till keys
			}
			if err := upsertRow(ctx, tx, t, cols, rec); err != nil {
				return fmt.Errorf("apply %s: %w", t.name, err)
			}
		}
	}
	return tx.Commit()
}

// applyPluginSettings replaces this till's GLOBAL plugin settings with the
// primary's, for exactly the plugins the bundle mentions. Register/user-scoped
// rows never travel and are never touched, and a replica-only plugin (one the
// primary doesn't have) keeps its local global settings. Rows for plugins not
// installed on this till are skipped — plugin_settings FKs plugins, and
// replicas install plugins from the marketplace themselves; the rows land on
// the pull after the install. Delete-then-insert per plugin (not a prune):
// it propagates key deletion within a plugin without seeing per-till rows,
// which are absent from the bundle by design.
//
// Defensive dedupe (ut-docs#807): a primary that hasn't yet applied
// migration 053's ux_plugin_settings_global index (e.g. still running an
// older release, or mid-upgrade) can still hand this a bundle carrying two
// global rows for the same (plugin_id, key) — the schema-level backstop
// only stops a NEW duplicate from forming on a till that already has the
// index, it does nothing to sanitize what an un-upgraded primary sends.
// Without dedupeGlobalPluginSettings, the second row's insert below hits
// that same index on THIS (already-upgraded) till and aborts the entire
// admin-bundle apply — not just plugin_settings, but catalog, users, tax
// codes, payment methods and the till roster too, on every pull, until the
// primary itself is fixed. Deduping here turns that shop-wide outage into
// a self-healing collapse: the loser is dropped, the winner applies, and
// the primary's own next migration/repair (052) is what actually cleans up
// its source data — this is just refusing to import a primary's bug.
func applyPluginSettings(ctx context.Context, tx *sql.Tx, t adminTable, recs []map[string]any) error {
	recs = dedupeGlobalPluginSettings(recs)
	installed := map[string]bool{}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM plugins`)
	if err != nil {
		return fmt.Errorf("apply plugin_settings: %w", err)
	}
	ids, err := scanGenericCols(rows, []string{"id"})
	rows.Close()
	if err != nil {
		return fmt.Errorf("apply plugin_settings: %w", err)
	}
	for _, rec := range ids {
		installed[fmt.Sprint(rec["id"])] = true
	}

	cleared := map[string]bool{}
	for _, rec := range recs {
		pid := fmt.Sprint(rec["plugin_id"])
		if cleared[pid] {
			continue
		}
		cleared[pid] = true
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM plugin_settings WHERE scope = 'global' AND plugin_id = ?`, pid); err != nil {
			return fmt.Errorf("apply plugin_settings: %w", err)
		}
	}

	cols, err := tableColumns(ctx, tx, t.name)
	if err != nil {
		return err
	}
	for _, rec := range recs {
		if !installed[fmt.Sprint(rec["plugin_id"])] {
			continue
		}
		if fmt.Sprint(rec["scope"]) != "global" {
			continue // defense in depth: a primary must never write per-till scopes
		}
		if err := upsertRow(ctx, tx, t, cols, rec); err != nil {
			return fmt.Errorf("apply plugin_settings: %w", err)
		}
	}
	return nil
}

// dedupeGlobalPluginSettings keeps one winning row per (plugin_id, key)
// among scope='global' bundle rows, dropping the rest before they ever
// reach an INSERT. Non-global rows pass through untouched — scope='global'
// rows are the only ones with a real uniqueness constraint on (plugin_id,
// key) (ux_plugin_settings_global, migration 053); register/user-scoped
// rows are additionally distinguished by scope_id, so deduping them the
// same way would wrongly conflate distinct rows. See applyPluginSettings's
// comment for why this exists at all.
//
// A drop is logged (not silent): converting a loud whole-bundle-abort
// failure into a quiet one otherwise leaves no signal anywhere that a
// primary is shipping duplicate rows — an operator/support engineer would
// have no way to notice until the primary's own migration 052 repairs it,
// same reasoning deleteMissing's own Warnf (below) already applies to a
// row it can't prune.
func dedupeGlobalPluginSettings(recs []map[string]any) []map[string]any {
	winners := map[string]map[string]any{}
	order := make([]string, 0, len(recs))
	out := make([]map[string]any, 0, len(recs))
	for _, rec := range recs {
		if fmt.Sprint(rec["scope"]) != "global" {
			out = append(out, rec)
			continue
		}
		k := fmt.Sprint(rec["plugin_id"]) + "\x1f" + fmt.Sprint(rec["key"])
		cur, seen := winners[k]
		if !seen {
			order = append(order, k)
			winners[k] = rec
			continue
		}
		if pluginSettingWins(rec, cur) {
			logging.L().Warnf("sync pull: dropping stale duplicate global plugin_settings row id=%v for plugin_id=%v key=%v (keeping id=%v) — the primary is shipping duplicates, likely running pre-migration-053",
				cur["id"], rec["plugin_id"], rec["key"], rec["id"])
			winners[k] = rec
		} else {
			logging.L().Warnf("sync pull: dropping stale duplicate global plugin_settings row id=%v for plugin_id=%v key=%v (keeping id=%v) — the primary is shipping duplicates, likely running pre-migration-053",
				rec["id"], rec["plugin_id"], rec["key"], cur["id"])
		}
	}
	for _, k := range order {
		out = append(out, winners[k])
	}
	return out
}

// pluginSettingWins reports whether candidate should replace incumbent as
// the surviving row for one (plugin_id, key) pair, mirroring migration
// 052's own tiebreak so a replica applying a duplicate-carrying bundle
// converges on the same winner the primary's own repair migration would
// pick: newer updated_at wins; a tie (updated_at collides at
// second-resolution) breaks on id, both TEXT columns compared the same
// lexicographic way SQL's ORDER BY does for them.
func pluginSettingWins(candidate, incumbent map[string]any) bool {
	cu, iu := fmt.Sprint(candidate["updated_at"]), fmt.Sprint(incumbent["updated_at"])
	if cu != iu {
		return cu > iu
	}
	return fmt.Sprint(candidate["id"]) > fmt.Sprint(incumbent["id"])
}

// pkOf renders a composite key for set membership.
func pkOf(t adminTable, rec map[string]any) string {
	parts := make([]string, len(t.pk))
	for i, c := range t.pk {
		parts[i] = fmt.Sprint(rec[c])
	}
	return strings.Join(parts, "\x1f")
}

func deleteMissing(ctx context.Context, tx *sql.Tx, t adminTable, recs []map[string]any) error {
	keep := make(map[string]bool, len(recs))
	for _, rec := range recs {
		keep[pkOf(t, rec)] = true
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT `+strings.Join(t.pk, ", ")+` FROM `+t.name)
	if err != nil {
		return fmt.Errorf("prune %s: %w", t.name, err)
	}
	existing, err := scanGenericCols(rows, t.pk)
	rows.Close()
	if err != nil {
		return fmt.Errorf("prune %s: %w", t.name, err)
	}
	for _, rec := range existing {
		if keep[pkOf(t, rec)] {
			continue
		}
		where := make([]string, len(t.pk))
		args := make([]any, len(t.pk))
		for i, c := range t.pk {
			where[i] = c + " = ?"
			args[i] = rec[c]
		}
		_, err := tx.ExecContext(ctx,
			`DELETE FROM `+t.name+` WHERE `+strings.Join(where, " AND "), args...)
		if err == nil {
			continue
		}
		// FK-blocked (row referenced by local sales history): retire it in
		// place — deactivate, and release its UNIQUE values (sku, username,
		// …) so the primary's row can still upsert. The CASE keeps the
		// mangle idempotent across pulls.
		if t.hasIsActive || len(t.unique) > 0 {
			var sets []string
			if t.hasIsActive {
				col := t.activeCol
				if col == "" {
					col = "is_active"
				}
				sets = append(sets, col+" = 0")
			}
			pk := t.pk[0]
			for _, c := range t.unique {
				sets = append(sets, fmt.Sprintf(
					"%s = CASE WHEN %s LIKE '%%~' || %s THEN %s ELSE %s || '~' || %s END",
					c, c, pk, c, c, pk))
			}
			if _, derr := tx.ExecContext(ctx,
				`UPDATE `+t.name+` SET `+strings.Join(sets, ", ")+` WHERE `+strings.Join(where, " AND "),
				args...); derr == nil {
				continue
			}
		}
		logging.L().Warnf("sync pull: cannot prune %s %v (kept): %v", t.name, args, err)
	}
	return nil
}

// upsertRow inserts or fully updates one row. Column names are validated
// against the live schema, never taken from the wire.
func upsertRow(ctx context.Context, tx *sql.Tx, t adminTable, cols []string, rec map[string]any) error {
	isPK := map[string]bool{}
	for _, c := range t.pk {
		isPK[c] = true
	}
	skip := map[string]bool{}
	for _, c := range t.skipCols {
		skip[c] = true
	}
	redact := map[string]bool{}
	for _, c := range t.redactCols {
		redact[c] = true
	}
	var names []string
	var args []any
	var sets []string
	for _, c := range cols {
		if skip[c] {
			continue // till-local column; ignore even if an older primary sends it
		}
		if redact[c] {
			// ut-docs#405: unlike skipCols, a redacted column is force-set
			// to NULL on every apply — never taken from rec (which never
			// has it anyway, DumpAdmin already stripped it) and never left
			// untouched on a row that already existed locally with a real
			// value (e.g. from a full-DB-snapshot enrolment).
			names = append(names, c)
			args = append(args, nil)
			if !isPK[c] {
				sets = append(sets, c+" = NULL")
			}
			continue
		}
		v, ok := rec[c]
		if !ok {
			continue // column the primary doesn't know (newer replica schema)
		}
		names = append(names, c)
		args = append(args, v)
		if !isPK[c] {
			sets = append(sets, c+" = excluded."+c)
		}
	}
	if len(names) == 0 {
		return nil
	}
	q := `INSERT INTO ` + t.name + ` (` + strings.Join(names, ", ") + `) VALUES (` +
		strings.TrimSuffix(strings.Repeat("?, ", len(names)), ", ") + `) ON CONFLICT (` +
		strings.Join(t.pk, ", ") + `) DO `
	if len(sets) == 0 {
		q += `NOTHING`
	} else {
		q += `UPDATE SET ` + strings.Join(sets, ", ")
	}
	_, err := tx.ExecContext(ctx, q, args...)
	return err
}

// tableColumns lists a table's live columns (SELECT * with LIMIT 0).
func tableColumns(ctx context.Context, tx *sql.Tx, table string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT * FROM `+table+` LIMIT 0`)
	if err != nil {
		return nil, fmt.Errorf("columns of %s: %w", table, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("columns of %s: %w", table, err)
	}
	return cols, nil
}

func scanGeneric(rows *sql.Rows) ([]map[string]any, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	return scanGenericCols(rows, cols)
}

// scanGenericCols reads rows into maps; []byte becomes string so the JSON
// payload carries text, not base64.
func scanGenericCols(rows *sql.Rows, cols []string) ([]map[string]any, error) {
	out := []map[string]any{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		rec := make(map[string]any, len(cols))
		for i, c := range cols {
			if b, ok := vals[i].([]byte); ok {
				rec[c] = string(b)
			} else {
				rec[c] = vals[i]
			}
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}
