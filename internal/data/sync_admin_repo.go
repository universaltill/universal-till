package data

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/universaltill/universal-till/internal/logging"
)

// SyncAdminRepo serves LAN sync increment D2b (ADR-0011): the primary dumps
// its admin-managed state (catalog, users, settings, translations) as one
// bundle; a replica applies it wholesale — primary wins. A whole-bundle
// fingerprint replaces per-table cursors so deletes propagate too.
type SyncAdminRepo struct {
	db *sql.DB

	// ut-docs#1368: DumpAdmin's last result, keyed on the
	// sync_admin_version.generation it was scanned under (migration 022's
	// triggers bump that counter on every write to any adminTables entry).
	// An unchanged-poll from a replica then costs one single-row SELECT
	// instead of the full 34-table scan + marshal + hash. Per process and
	// per repo instance; the counter itself lives in the DB, so a second
	// instance (or a restart) just starts cold and converges.
	mu         sync.Mutex
	cacheGen   int64
	cache      AdminBundle
	cacheFP    string
	cacheValid bool
}

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
	stickyNonBlankCols []string // columns where a blank (NULL/'') incoming
	// value never overwrites an existing non-blank local value, even though
	// the column otherwise travels normally (unlike skipCols, a real
	// incoming value still wins and does write through — this only refuses
	// a blank one). Mirrors CatalogRepo.UpdateVariant's own long-standing
	// SET sku = COALESCE(NULLIF(?, ''), sku): blank has never meant "clear
	// it" anywhere else in this codebase, and the generic upsert below is
	// the one path that didn't follow that rule (ut-docs#2246) — a still-
	// behind primary's blank sku kept invalidating item_variants.sku
	// backfillCodelessSyncedVariants had already fixed, on every poll.
}

// adminTables is the shop-wide state a replica mirrors. Deliberately NOT
// here: inventory/stock (additive movements, D3), sales, sessions,
// item_images (files don't travel — D2 limit).
//
// registers and stock_locations were excluded here from ut-docs#1584 until
// ut-docs#1590: both `name` fields read as shop-wide, but until #1590 the
// admin pages that create them (`/registers`, `/locations`) had no
// primary-only check, so a manager could create either directly on a
// satellite — and a freshly-created row has no shift/sale/inventory
// history yet to FK-block ApplyAdmin's deleteMissing from erasing it
// outright on the very next admin pull. #1590 closed that gap
// (POSRepo.CreateRegister/CreateStockLocation and their rename/activate
// siblings now refuse on a replica via requirePrimary in
// registers_page.go/locations_page.go, same pattern as
// plugins_store_page.go's replica_use_primary gate), so both are now safe
// to sync — see TestAdminApplyLeavesRegistersAndStockLocationsUntouched's
// replacement, TestAdminDumpApplyRoundTrip_RegistersAndStockLocations, for
// the regression guard covering the new behaviour. Registers' shop-wide
// need was already met at enrollment time regardless:
// CreateRegisterForEnrolment runs on the PRIMARY (POST /api/sync/enroll),
// so a joining till's own register already exists shop-wide before the
// till's first sale, via the initial full-DB-snapshot join rather than
// ongoing sync.
//
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
	// ut-docs#1610: brands gained is_active in migration 004 so an FK-blocked
	// prune retires the row (is_active = 0) like every other table here whose
	// UNIQUE column doubles as its display name, instead of only mangling
	// name and leaving nothing that marks the row as retired.
	{name: "brands", pk: []string{"id"}, hasIsActive: true, unique: []string{"name"}},
	// ut-docs#1898/#1610: categories gained is_active in migration 017 so an
	// FK-blocked prune retires the row (is_active = 0) instead of leaving it
	// permanently active with no signal it was ever pruned. No `unique` entry
	// here (unlike brands/tax_codes/users/stock_locations/registers): unlike
	// those five, categories.name carries no DB UNIQUE constraint, so the
	// mangle step deleteMissing runs for `unique` columns would have nothing
	// to free and nothing to protect — only the is_active flag applies.
	{name: "categories", pk: []string{"id"}, hasIsActive: true},
	{name: "customers", pk: []string{"id"}, unique: []string{"loyalty_no"}},
	// plugin_id is till-local derived state (which plugin installed on THIS
	// till owns the method) — importing it re-hijacks a repaired built-in
	// from a not-yet-upgraded primary (ADR-0031).
	{name: "payment_methods", pk: []string{"id"}, hasIsActive: true, unique: []string{"name"}, skipCols: []string{"plugin_id"}},
	{name: "users", pk: []string{"id"}, hasIsActive: true, unique: []string{"username"}},
	// ut-docs#1590: registers and stock_locations became safe to sync once
	// /registers and /locations gated create/rename/activate to
	// primary-only (see this var's own top comment for the full trace).
	// stock_locations MUST precede registers here: registers.location_id
	// FKs onto stock_locations(id), upserts run forward through this list
	// (ApplyAdmin phase 2), and deletes run in reverse (phase 1) — so this
	// order is also what lets a retired/deleted register clear before its
	// stock location does.
	{name: "stock_locations", pk: []string{"id"}, hasIsActive: true, unique: []string{"name"}},
	{name: "registers", pk: []string{"id"}, hasIsActive: true, unique: []string{"name"}},
	// ut-docs#1554: role_permissions is runtime-mutable (AuthRepo.
	// SetRolePermission, from the permission matrix editor) and was missing
	// from this list entirely — a manager revoking e.g. `refund` from
	// cashiers on the primary left every satellite still granting it, with
	// nothing to catch the drift. This is the security-relevant half of
	// #1554; roles and permission_actions are included alongside it even
	// though today they're migration-seeded only (no runtime INSERT
	// anywhere) and so happen to already match across tills — but that
	// match holds only "because every till seeds them identically," the
	// exact fragility #1554 calls out (a future custom-roles feature, or
	// two tills mid-rollout on different migration versions, would break
	// it silently). roles/permission_actions have no FK dependencies of
	// their own; role_permissions FKs onto both, so it must apply after —
	// ordered accordingly here.
	{name: "roles", pk: []string{"role"}},
	{name: "permission_actions", pk: []string{"action"}},
	{name: "role_permissions", pk: []string{"role", "action"}},
	{name: "items", pk: []string{"id"}, hasIsActive: true, unique: []string{"sku"}},
	{name: "item_barcodes", pk: []string{"barcode"}},
	// stickyNonBlankCols: []string{"sku"} — ut-docs#2246: without this, a
	// primary still behind on ut-docs#1900 (or its own #2230 fix) that keeps
	// sending a blank sku for a variant this replica already backfilled
	// (see backfillCodelessSyncedVariants below) would have that backfill
	// undone on every single poll, changing the scannable code a shelf
	// label was just printed with. A real, non-blank incoming sku still
	// overwrites normally — this only refuses a blank one.
	{name: "item_variants", pk: []string{"id"}, hasIsActive: true, unique: []string{"sku"}, stickyNonBlankCols: []string{"sku"}},
	{name: "variant_barcodes", pk: []string{"barcode"}},
	// ut-docs#1900: reusable option sets (migration 017) are catalog
	// structure of exactly the same shop-wide kind as item_modifier_groups
	// below — the generated variants themselves already travel as
	// item_variants rows above, and a satellite that had the rows but not
	// the sets/links they came from would show the item's range with no
	// record of what it was generated from. Mutation is primary-only
	// (catalog/handlers.go's requirePrimary on every option-set route),
	// which is what makes syncing safe, same as #1667's reasoning for the
	// modifier tables. Ordered for their FKs: option_set_values ->
	// option_sets; item_option_sets -> items + option_sets;
	// item_variant_options -> item_variants + option_set_values (all
	// applied after items/item_variants above). option_sets has a real
	// is_active plus UNIQUE(name), so it retires like items on an FK-blocked
	// prune; option_set_values' UNIQUE is (option_set_id, value), and
	// mangling `value` alone keeps that pair unique too.
	{name: "option_sets", pk: []string{"id"}, hasIsActive: true, unique: []string{"name"}},
	{name: "option_set_values", pk: []string{"id"}, unique: []string{"value"}},
	{name: "item_option_sets", pk: []string{"item_id", "option_set_id"}},
	{name: "item_variant_options", pk: []string{"variant_id", "option_set_value_id"}},
	{name: "related_items", pk: []string{"item_id", "related_item_id"}},
	// ut-docs#1667: same shape as #1546 (tables/kitchen_stations) — catalog
	// structure that reads shop-wide but was missing from this list
	// entirely, found while classifying every table for #1586's schema-drift
	// guard. item_modifier_groups is shop-wide and, since ADR-0101
	// (ut-docs#2399, migration 034), no longer FKs onto items at all — it
	// sits after items only because every link table below it does, and
	// keeping the block together reads better; item_modifier_options FKs
	// onto item_modifier_groups(id), so it must apply after that. Both
	// have a real is_active column (the app's
	// own CreateGroup/UpdateGroup and CreateOption/UpdateOption never hard-
	// delete), so hasIsActive mirrors items/item_variants above. Mutation is
	// now gated primary-only in catalog/handlers.go's requirePrimary (same
	// #1590 pattern as registers/locations), which is what makes this safe
	// to sync: without that gate, a satellite-created modifier would just
	// vanish on the next admin pull instead of failing loudly up front.
	// ModifierRepo.DeleteGroup DOES hard-delete and IS wired, to POST
	// /api/catalog/modifier-group/delete (ADR-0101 Decision 2), behind the
	// same requirePrimary gate; DeleteOption is still unwired — whoever
	// wires it up must gate it the same way.
	{name: "item_modifier_groups", pk: []string{"id"}, hasIsActive: true},
	// ADR-0090 / ut-docs#2013: which items use which modifier group, now
	// that a group is shareable — catalog structure of exactly the same
	// shop-wide kind as item_option_sets above, and a satellite that had
	// the groups but not the links would show every item with no
	// customization step at all. FKs onto items(id) and
	// item_modifier_groups(id), both applied above, so it sits here; a pure
	// link row with a composite PK and no is_active, same as
	// item_option_sets (an FK-blocked prune can't happen — nothing FKs onto
	// a link row). Mutation is primary-only via the same requirePrimary
	// gate already covering item_modifier_groups/options. Its three
	// sync_admin_version triggers ship in migration 025.
	{name: "item_modifier_group_links", pk: []string{"item_id", "group_id"}},
	// ADR-0094 / ut-docs#1915: which CATEGORIES offer which modifier group,
	// so every item in the category inherits it at sale time — catalog
	// structure of exactly the same shop-wide kind as
	// item_modifier_group_links above, and a satellite that had the groups
	// but not the category links would offer every item in a category none
	// of its inherited groups. Pure link rows, no is_active, no UNIQUE
	// beyond the PK, same as item_modifier_group_links. FKs onto
	// categories(id) and item_modifier_groups(id), both already applied
	// above, so it must sit after them. Mutation is primary-only via the
	// same requirePrimary gate covering item_modifier_groups/options (the
	// editor surface itself is ut-docs#2284's card, not built by #1915).
	// Its three sync_admin_version triggers ship in migration 031.
	{name: "category_modifier_group_links", pk: []string{"category_id", "group_id"}},
	// ADR-0094 / ut-docs#1915: which items have DECLINED a category-inherited
	// modifier group (presence-only — opting back in is deleting the row).
	// Same shop-wide catalog structure as the two link tables above: a
	// satellite missing an opt-out would offer a group the primary's admin
	// deliberately removed from that item. FKs onto items(id) and
	// item_modifier_groups(id), both applied above. Its three
	// sync_admin_version triggers ship in migration 031.
	{name: "item_modifier_group_opt_outs", pk: []string{"item_id", "group_id"}},
	{name: "item_modifier_options", pk: []string{"id"}, hasIsActive: true},
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
	// ut-docs#1669: admin-manageable per-jurisdiction defaults, plain PK-only
	// like settings above — no is_active/soft-delete column, and none is
	// needed: CountrySettingsRepo.Delete() already restores a BUILTIN
	// country to its shipped defaults instead of removing the row (so a
	// builtin row is never actually absent from a primary's dump), and a
	// genuinely operator-created country being hard-deleted shop-wide on
	// prune is the correct behavior, not a gap to guard against. The
	// ADR-0040 archive_min_days floor is re-enforced separately in
	// ApplyAdmin below, since this generic upsert path bypasses
	// CountrySettingsRepo.Upsert()'s own validation.
	{name: "country_settings", pk: []string{"code"}},
	// pk is the surrogate uuid, not (plugin_id,key,scope,scope_id): that
	// table-level UNIQUE constraint includes scope_id, which is NULL on
	// global rows, and SQLite treats NULLs as distinct -- ON CONFLICT on
	// it would never fire. ux_plugin_settings_global (migration 053,
	// ut-docs#787) IS a real, targetable unique constraint for global
	// rows ((plugin_id, key) WHERE scope='global') -- applyPluginSettings
	// still doesn't upsert against it, relying on its own per-plugin
	// delete-then-insert instead (see applyPluginSettings' own comment).
	{name: "plugin_settings", pk: []string{"id"}},
	// ut-docs#1670: plugin_storage is generic plugin-private KV storage in
	// general (per-plugin local state), but FiscalRegisterDEPluginID's
	// fiscal_register:-prefixed rows back the shop-wide German TSE
	// till-register (ADR-0072/ut-docs#1106, FiscalRegisterDEStore) -- the
	// exact #1546 shape (shop-wide state missing from sync). Only that
	// plugin's fiscal_register:-prefixed rows travel: the dump filter below
	// (DumpAdmin) scopes on BOTH plugin_id and key prefix, and
	// applyFiscalRegisterStorage's scoped delete-then-insert (mirroring
	// applyPluginSettings just above) never touches any other row --
	// scoping by plugin_id too (not prefix alone) matches every other
	// plugin_storage accessor in this codebase, all of which key on
	// plugin_id (see FiscalRegisterDEPluginID's own doc comment in
	// fiscal_repo.go) -- without it, an unrelated plugin choosing a key
	// that happens to start with the same literal prefix would have its
	// own private state deleted and broadcast by this entry (found in
	// review, empirically confirmed with a throwaway third-party-plugin
	// row: it was pruned on apply and appeared in the primary's dump).
	{name: "plugin_storage", pk: []string{"plugin_id", "key"}},
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

// nonAdminTables is every OTHER table in the schema, one reason each for why
// it deliberately does NOT travel in the admin bundle. TestSchemaTablesAreClassified
// (schema_drift_test.go) fails CI the moment a new migration adds a table
// that ends up in neither this map nor adminTables above — the guard
// ut-docs#1586 asked for after ut-docs#1546 showed a shop-wide table
// (tables/kitchen_stations) can sit unsynced for a long time with nothing to
// catch it.
//
// A wrong "safe to exclude" verdict here reproduces that exact bug, just
// laundered through a passing CI check — so a table whose classification is
// genuinely uncertain is flagged with an open question and its own follow-up
// card below, NOT guessed into either list. Deciding those is explicitly
// this card's non-goal (ut-docs#1586): a guard classifying "should probably
// sync but doesn't yet" as settled would be exactly the rushed re-scoping
// ut-docs#1554 split out to avoid.
//
// Grouped by why, not alphabetically — the reason is what matters to a
// reviewer of a future migration, and several tables share one.
var nonAdminTables = map[string]string{
	// Already named in this var's own top comment above.
	"sessions":    "till-local login sessions — meaningless off the issuing till",
	"item_images": "item photos — files don't travel over this bundle (D2 limit)",

	// Migration runner's own bookkeeping (created by internal/db's migrator,
	// not a numbered migration file itself) — which migrations have applied
	// to THIS till's database. Syncing it would be circular in the same way
	// as sync_journal_quarantine/schema_lineage below.
	"schema_migrations": "this till's own applied-migrations record — migration-runner-internal, not app data",
	// ut-docs#1368: the one-row generation counter migration 022's triggers
	// bump on every write to an adminTables entry, read by DumpAdmin to
	// decide whether its cached bundle is still current. Sync-internal by
	// construction (it describes THIS database's admin state), and syncing
	// it would be circular: applying it on a replica would fire nothing
	// useful and its own value is meaningless off the till that counted it.
	"sync_admin_version": "DumpAdmin's cache-invalidation counter for this till's own admin tables — sync-internal, per-database",
	// ut-docs#2501: migration 042's one-row counter bumped by triggers on
	// price_history and item_images (the two sell-screen inputs
	// sync_admin_version does not cover), read with it by
	// SellScreenRepo.SellScreenVersion to invalidate internal/ui's
	// in-memory sell-screen tile cache. Same reasoning as
	// sync_admin_version above: it describes THIS database's own writes.
	"sell_screen_version": "sell-screen tile cache's invalidation counter for this till's own price_history/item_images writes — sync-internal, per-database",

	// Inventory/stock: D3's own additive-movement sync (ADR-0011), a
	// separate mechanism from this bundle — already named above.
	"inventory":               "current stock levels — D3's own additive-movement sync, not this bundle",
	"stock_movements":         "additive stock ledger — D3's own sync stream",
	"stock_movements_archive": "archived stock_movements rows — same D3 stream",

	// Sales and everything hung off a sale_id/receipt: already named above
	// ("sales"). A primary-wins bundle that prunes anything missing from the
	// sender's dump (deleteMissing) is the wrong shape for an append-only
	// ledger — a history row present locally but absent from the sender
	// would be pruned as if deleted, erasing real transaction history
	// instead of converging state. Each till keeps its own sales history;
	// cross-till reporting is export_repo.go's job, not LAN admin sync's.
	"sales":                                "per-till sale header — append-only ledger, wrong shape for this bundle",
	"sales_archive":                        "archived sales rows — same reasoning",
	"held_sales":                           "a parked/suspended sale basket on this till — a primary-wins bundle with deleteMissing pruning would erase a satellite's own genuinely-parked orders on every pull, worse than not syncing (ut-docs#1672); since ADR-0093 (ut-docs#1920) it crosses tills by a live, idempotent write-through to the primary at park/resume time instead (internal/pages/held_sale_sync_proxy.go + sync_held_sales.go), which this exclusion is precisely what leaves room for",
	"held_sales_archive":                   "archived held_sales rows — same reasoning",
	"held_sales_tombstones":                "ADR-0093 Amendment B (ut-docs#2712): the primary's own 24h record of held sales it deleted, answering a replica's POST /api/sync/held-sales/claim — only meaningful on the primary that wrote it, per-database, never synced",
	"demo_instance":                        "ADR-0113 §1.2 (ut-docs#2687): the one-row public-demo template flag the start gate reads — per-database by definition, and syncing it would carry demo mode onto a real till (which then refuses to start), never synced",
	"sale_lines":                           "sale line items, child of sales — same reasoning",
	"sale_lines_archive":                   "archived sale_lines — same reasoning",
	"sale_line_modifiers":                  "modifier selections on a sold line, child of sale_lines — same reasoning",
	"sale_line_modifiers_archive":          "archived sale_line_modifiers — same reasoning",
	"sale_charges":                         "per-sale service charges, child of sales — same reasoning",
	"sale_charges_archive":                 "archived sale_charges — same reasoning",
	"sale_discounts":                       "per-sale/line discounts, child of sales — same reasoning",
	"sale_discounts_archive":               "archived sale_discounts — same reasoning",
	"sale_links":                           "sale-to-original-sale links (refunds/reprints), child of sales — same reasoning",
	"sale_links_archive":                   "archived sale_links — same reasoning",
	"payments":                             "tender records, FK'd to sales — same append-only-ledger reasoning",
	"payments_archive":                     "archived payments — same reasoning",
	"invoices":                             "fiscal invoices/credit notes, FK'd to sales — same reasoning",
	"invoices_archive":                     "archived invoices — same reasoning",
	"fiscal_sign_starts":                   "in-flight German TSE signing state, keyed 1:1 on sale_id — per-sale, per-till",
	"fiscal_tse_signatures":                "completed TSE signatures, keyed 1:1 on sale_id — per-sale, per-till",
	"fiscal_tse_reconciled_signatures":     "TSE signatures a fiscal.sign.reconcile.ask sweep confirmed after the fact for a sale that completed unsigned (ADR-0077 D3), keyed 1:1 on sale_id — per-sale, per-till, same reasoning as fiscal_tse_signatures above; kept as its own table so no receipt render path ever reads it (D4)",
	"fiscal_device_receipts":               "what Turkey's ÖKC device printed for a sale, keyed 1:1 on sale_id — per-sale, per-till, same shape as fiscal_tse_signatures above",
	"fiscal_receipt_evidence":              "the QR payload + lines a fiscal.sign.ask signer returned for a sale (ut-docs#2880), keyed 1:1 on sale_id — per-sale, per-till, same reasoning as fiscal_tse_signatures above",
	"shifts":                               "cashier shift open/close, register-scoped — per-till operational history, same reasoning as sales",
	"shifts_archive":                       "archived shifts — same reasoning",
	"worker_allocations":                   "tip/service-charge pool allocations tied to a cashier + reset_batches — per-till operational history, same family as shifts/payments",
	"worker_allocations_archive":           "archived worker_allocations — same reasoning",
	"yuzde_usulu_pool_collections":         "Türkiye yüzde usulü pool collections a manager recorded on this till (ut-docs#988) — per-till operational history, exactly the same family as worker_allocations above (it is that ledger's collection-side twin)",
	"yuzde_usulu_pool_collections_archive": "archived yuzde_usulu_pool_collections — same reasoning",
	"report_archive":                       "this till's own X/Z report archive — per-till operational history, same reasoning as sales_archive",
	"sales_aggregate_uploads":              "which daily sales rollups THIS till has already delivered to the cloud (ut-docs#2535, content-hash ledger) — only the primary uploads, and syncing it would make a replica believe it had sent days it never did",
	"voucher_transactions":                 "per-sale voucher issue/redemption ledger — same append-only reasoning as payments; stays per-till exactly like vouchers below (ut-docs#1668) — this till's own local ledger row, never dumped/applied. Its 'redemption' rows double as the cross-till idempotency key, (voucher_id, sale_id), ADR-0084 — see the vouchers entry below",

	// Plugin install machinery — already named above. The installed SET
	// travels as its own separately-fingerprinted bundle (SyncPluginsRepo,
	// GET /api/sync/plugins) and each replica re-verifies every listing
	// itself; these underlying rows never travel because a row without the
	// Ed25519-verified plugin FILES would leave a replica with a phantom
	// plugin.
	"plugins":               "installed-plugin rows — travel via SyncPluginsRepo's own bundle, not this one",
	"plugin_catalog":        "cached marketplace listing metadata — re-fetched from the marketplace, never synced till-to-till",
	"plugin_entries":        "plugin-contributed menu/hook entries — recreated on re-install, not synced",
	"plugin_hooks":          "plugin-contributed hooks — recreated on re-install, not synced",
	"plugin_install_status": "install-progress bookkeeping — SyncPluginsRepo's own source table",
	"plugin_permissions":    "granted permissions for one installed plugin instance — recreated on re-install",

	// Sync's own internal bookkeeping — syncing the sync mechanism's state
	// to itself would be circular.
	"sync_journal_quarantine": "this till's own quarantined-sale bookkeeping — sync-internal",
	"schema_lineage":          "this till's own migration/reset marker — sync-internal schema bookkeeping",
	"pending_pairings":        "in-flight LAN pairing requests — ephemeral, till-local",
	"sync_asset_ledger":       "which asset files this replica downloaded from the main (ut-docs#2785 prune bookkeeping) — sync-internal",

	// Live/ephemeral operational state: a periodic, primary-wins bundle is
	// the wrong mechanism for a lock or an event stream — applying a stale
	// snapshot of either would actively misbehave (a claim from a till
	// that's since gone offline would look permanently locked; a replayed
	// status event would re-fire a KDS notification).
	"table_claims":        "live table-service lock — ephemeral, re-established on demand, never meant to survive a periodic snapshot",
	"order_status_events": "live KDS status-change event stream — operational, not admin config",
	"reset_batches":       "this till's own EOD/Z-report reset marker — sync-adjacent bookkeeping, same family as report_archive",
	// ut-docs#582: self-order kiosk "pay at counter" orders — per-till
	// operational state (what THIS till's kiosk is waiting to hand over),
	// never a sale (no FK to sales, no money columns). Cross-till sync is
	// an explicit non-goal for v1 (a staff board at each till, same as
	// order_status_events above), and a primary-wins deleteMissing bundle
	// would be the wrong shape for it anyway — same held_sales/table_claims
	// reasoning: a satellite's own in-flight counter order must not be
	// pruned just because a periodic snapshot from elsewhere didn't carry it.
	"kiosk_counter_orders": "self-order kiosk pay-at-counter orders — per-till operational state, never a sale, no cross-till sync (v1 non-goal)",

	// Local bookkeeping with no shop-wide meaning.
	"issue_reports_sent": "dedup record of bug reports already sent FROM this till",
	"audit_log":          "this till's own local action log (including per-till-scoped settings changes — see PerTillSettingPrefixes); a shop-wide combined audit view is a separate concern, not LAN admin sync's job",
	// ut-docs#1465: pre-tender void/comp/waste event log — same reasoning
	// as audit_log immediately above (an append-only, per-till action
	// record, not shop-wide admin config), plus the sales-family reasoning
	// above it: a primary-wins bundle with deleteMissing pruning is the
	// wrong shape for an append-only ledger. Cross-till shrinkage reporting
	// (the "Shrinkage & Loss" reports tab) reads only this till's own rows,
	// same as every other per-till operational-history table in this list.
	"shrinkage_events": "pre-tender void/comp/waste event log — append-only per-till history, same reasoning as audit_log/sales",

	// Resolved classification (ADR-0099, ut-docs#2348, closing the question
	// ut-docs#1671 deferred): correctly excluded. NOT a pure append-only
	// audit trail (AppendPriceHistoryItem/Variant UPDATE the prior row's
	// ends_at, and item deletion DELETEs rows), NOT inert to checkout
	// (ResolveCurrentPrice consults an open price_history row BEFORE items'
	// synced price, so it can override it), and — the reason it doesn't
	// simply join adminTables — an ever-growing ledger with no natural
	// ceiling, unlike every current-state table in that list. Resolved by
	// NOT syncing the table at all: invalidateStalePriceHistoryOnSync (the
	// post-apply step at the end of ApplyAdmin) closes every locally-open
	// price_history row for a synced item/variant on the next admin bundle
	// that actually changes (ApplyAdmin only runs when the primary's admin
	// fingerprint moves — sync_admin.go's `!Unchanged` check — not
	// literally every poll; a primary-side price edit always moves it,
	// since items/item_variants are themselves admin tables), so a
	// satellite can never keep charging a stale override once the primary
	// has moved the underlying price. Guarded by
	// scripts/ci/guard-price-history-sync.sh (every SQL write to this table
	// must sit in an explicitly allowlisted function). Still open, NOT
	// closed by this: the cloud SetPrice directive path is not
	// primary-gated today (ut-docs#2353), so a satellite can write its own
	// row directly and the invalidation only cleans that up on the next
	// admin bundle that changes — an unbounded window, not "next poll".
	"price_history": "ever-growing price-change ledger, never synced (ADR-0099); a satellite's stale open override is closed by invalidateStalePriceHistoryOnSync on every ApplyAdmin instead, so checkout falls through to the synced items/item_variants price",

	// Resolved classification (ut-docs#1668): correctly excluded, same
	// concurrency reasoning ut-docs#1554 gave role_permissions — a periodic
	// primary-wins dump/apply on a balance that can change between polls
	// risks clobbering a redemption made on a satellite since the last
	// pull, or reverting a spent voucher back to its old balance. Resolved
	// by NOT syncing this table at all. Instead, two mechanisms coexist
	// deliberately, not by accident (ADR-0084, ut-docs#1716):
	//
	//   1. At tender time, a replica RESERVES a redemption on the primary
	//      (POST /api/sync/vouchers/{id}/redeem, sync_vouchers.go) — a real
	//      debit of the primary's balance, run through
	//      DebitVoucherForRedemption's guarded UPDATE on the primary's own
	//      database so two tills racing for one balance serialize there.
	//   2. Later, the same sale reaches the primary the ordinary way — the
	//      sales journal (applyJournal → pos.CompleteSale) — carrying the
	//      SAME sale id the reservation was made under.
	//
	// What keeps (1) and (2) from debiting twice is the idempotency key:
	// one 'redemption' voucher_transactions row per (voucher_id, sale_id),
	// enforced by ux_voucher_tx_redemption_once (migration 012).
	// ReserveVoucherRedemption writes that row; pos.CompleteSale checks it
	// (VoucherRedemptionRecorded) before its own debit and skips a
	// redemption the reservation already applied. A reservation whose
	// tender then fails is released by the same till in the same request
	// (POST .../release) before any local sale row exists, so no journal
	// entry for it can ever follow. ut-docs#1668's own first draft had the
	// write-through WITHOUT this key and was reverted for double-debiting
	// every online redemption (round-2 review, 2026-09-07); #1668 then
	// shipped read-only, and ADR-0084 supplied the key. The offline
	// fallback is untouched: a replica that cannot reach the primary
	// validates locally and the replay force-applies as before
	// (AllowVoucherOverdraft, ut-docs#1053).
	"vouchers": "shop-wide voucher balance, runtime-mutable across tills — kept per-till; cross-till redemption is reserved live on the primary and keyed (voucher_id, sale_id) for the journal replay (ADR-0084), not a synced table",
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

// AutoUpdateLastAttemptSettingsKey is the date THIS till last tried its
// nightly unattended update (pages.autoUpdateTick). Per-till (ut-docs#2726):
// synced shop-wide, the main till's attempt reached every replica as "already
// attempted today" and cancelled theirs. Exported so internal/pages uses the
// same literal instead of re-typing it.
const AutoUpdateLastAttemptSettingsKey = "update.auto_last_attempt"

// UpdateFollowAttemptedSettingsKey / UpdateFollowErrorSettingsKey are THIS
// replica's own record of following its main till's version (ut-docs#2738,
// pages.followTick): the target it last tried, and how that try ended ("" =
// staged, "applying", or "failed:<code>"). Per-till for the same reason as
// AutoUpdateLastAttemptSettingsKey: synced, one till's "already tried 1.4.0"
// would stop every other replica from trying it.
const (
	UpdateFollowAttemptedSettingsKey = "update.follow_attempted"
	UpdateFollowErrorSettingsKey     = "update.follow_error"
)

// ThemeSettingsKey is the UI theme of THIS till (ut-docs#2783): per station,
// never admin-synced — a self-order kiosk, a back-office screen and a
// cashier's till may each want their own look (product-owner decision
// 2026-09-25; most POS apps apply the theme per device). While it synced
// shop-wide, a theme picked on a joined till was silently overwritten by the
// main till's value on the next admin pull whose fingerprint moved, and the
// open page's /ui/theme-sync poll then flipped the screen back. Exported so
// internal/pages/common's KeyTheme can be asserted equal to it
// (TestThemeSettingsKeyMatchesCommon) — common already imports this package,
// so it cannot be imported the other way.
const ThemeSettingsKey = "theme"

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
// is a true equality test, so this works as an exact match. So is
// AutoUpdateLastAttemptSettingsKey (ut-docs#2726); the auto-update schedule
// itself stays shop-wide.
//
// The marketplace entries (ut-docs#2730) are this till's own cloud identity
// (db.TillCloudIdentityPrefixes) plus the marketplace signing key it pinned on
// first sight (a synced value would let another till silently replace a
// pinned trust anchor). Syncing them made every replica heartbeat as the
// main till's device, carrying the main till's store token. Both ends
// enforce it: DumpAdmin never sends these rows and ApplyAdmin never applies
// them (a pre-fix main till still sends them). The store-level marketplace
// keys — store_id, merchant_id, telemetry_opt_in, auto_register_opt_in — are
// the same for every till of the shop and keep syncing; the main till
// registers each replica's own device (internal/enroll, POST
// /api/sync/cloud-device) without any credential crossing the LAN.
var PerTillSettingPrefixes = []string{
	"sync.", "printer.", "display.", "reports.eod_", FiscalPendingSignRetriesSettingsKey, AutoUpdateLastAttemptSettingsKey,
	// ut-docs#2738: this replica's follow bookkeeping (full keys, like the
	// one above). sync.main_version, the main till's pinged version, is
	// already covered by "sync.".
	UpdateFollowAttemptedSettingsKey, UpdateFollowErrorSettingsKey,
	// ut-docs#2783: this till's own theme. A full key used as a prefix, like
	// the two above; no other settings key starts with "theme".
	ThemeSettingsKey,
	// The same three entries as db.TillCloudIdentityPrefixes (the join
	// snapshot's redaction; internal/db can't import this package) —
	// TestPerTillSettingsCoverTillCloudIdentity keeps them in step.
	"marketplace.device_", "marketplace.token", "marketplace.enrolled_at",
	"marketplace.public_key",
	// ut-docs#2792: this till's own cache of its cloud entitlement (ADR-0060
	// §4, internal/entitlement's Key* constants), rewritten with a fresh
	// last_confirmed_at on every cloud sync tick. Synced, that timestamp
	// moved the main till's admin fingerprint every tick and sent every
	// replica into a full admin re-pull + plugin check every ~2 min. A till
	// with its own store token caches from its own cloud sync; a token-less
	// replica gets the main till's cache on the vouch answer
	// (enroll.applyRelayedEntitlement, POST /api/sync/cloud-device).
	"entitlement.",
	// ut-docs#2821, ADR-0117 §2: cloud.link_tier / cloud.link_mode, written
	// by the same cacheEntitlement call (same transaction) as entitlement.*
	// above — every valid sync-response block rewrites them, even when the
	// tier itself doesn't change. Same fingerprint-churn reasoning, same
	// fix: per-till, not admin-synced. Unlike entitlement.plan there is no
	// dedicated relay to a token-less replica today — only the main till
	// dials the cloud-link socket (ADR-0117 §1), so a replica never needs
	// its own cached value.
	"cloud.link_",
}

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
//
// ut-docs#1368: the scan only runs when sync_admin_version.generation has
// moved since the cached bundle was built — otherwise the cached bundle is
// returned as-is, and none of the admin tables are touched. See ensureCached
// for the generation-ordering and mutex reasoning shared with AdminFingerprint.
//
// Callers get the cached bundle's row maps, not copies — the handler only
// encodes it. The outer Tables map is a fresh shallow copy so a caller that
// adds/removes a table entry can't corrupt what the next poll is served.
func (r *SyncAdminRepo) DumpAdmin(ctx context.Context) (AdminBundle, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	_, bundle, err := r.ensureCached(ctx)
	if err != nil {
		return AdminBundle{}, err
	}
	return bundle.shallowCopy(), nil
}

// AdminFingerprint is the cheap half of an admin-sync poll (ut-docs#1368
// follow-up): on a cache hit it costs one single-row SELECT and a field
// read — no table scan, no JSON marshal, no SHA-256. Before this method
// existed, the HTTP handler always called DumpAdmin (cheap after the
// generation-cache fix) and THEN bundle.Fingerprint() unconditionally on
// the result — which still re-marshaled and re-hashed the whole bundle on
// every single poll, measured at ~25ms against a ~25ms pre-fix scan: the
// generation cache alone only removed the DB-scan half of the per-poll
// cost, not the marshal+hash half the card also asked for. The handler now
// calls this FIRST; DumpAdmin is only called when the caller's `?have=`
// doesn't match this value, and that second call then hits the same cache
// this call just populated (or served from), so it never re-scans either.
func (r *SyncAdminRepo) AdminFingerprint(ctx context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	fp, _, err := r.ensureCached(ctx)
	return fp, err
}

// ensureCached is the shared core of DumpAdmin and AdminFingerprint. Caller
// must hold r.mu. Returns the bundle's fingerprint and content for the
// CURRENT generation, computing and caching both together on a miss so a
// fingerprint-only caller and a bundle caller converge on one scan instead
// of each hashing or scanning independently.
//
// The generation is read BEFORE the scan, never after: a write committing
// between the two then leaves the cache keyed on the OLDER generation (one
// redundant rescan next poll), whereas reading it after could key a
// pre-write scan on the post-write generation and serve stale rows until
// the next unrelated change. The mutex is held across the scan on purpose:
// concurrent replica polls on a cache miss share one scan instead of each
// running their own.
func (r *SyncAdminRepo) ensureCached(ctx context.Context) (fp string, bundle AdminBundle, err error) {
	gen, tracked := r.adminGeneration(ctx)
	if tracked && r.cacheValid && r.cacheGen == gen {
		return r.cacheFP, r.cache, nil
	}
	bundle, err = r.scanAdmin(ctx)
	if err != nil {
		return "", AdminBundle{}, err
	}
	fp = bundle.Fingerprint()
	if tracked {
		r.cache, r.cacheGen, r.cacheFP, r.cacheValid = bundle, gen, fp, true
	} else {
		// No counter row to key on (a hand-edited DB — migrations always
		// seed it): never cache, or a later call would hit on the same
		// "no row" reading and serve stale content forever.
		r.cacheValid = false
	}
	return fp, bundle, nil
}

// AdminGeneration is the cheap change marker on its own (one single-row
// SELECT): the main-till link (ADR-0114 §2) polls it to nudge linked tills
// the moment any admin table — users, roles and PINs included (#2731) —
// changes, however it was written. tracked is false when the counter row is
// missing.
func (r *SyncAdminRepo) AdminGeneration(ctx context.Context) (gen int64, tracked bool) {
	return r.adminGeneration(ctx)
}

// adminGeneration reads the cheap change marker. tracked is false when the
// row is missing, which callers treat as "always rescan" — never an error,
// so a damaged counter can't take the whole sync path down.
func (r *SyncAdminRepo) adminGeneration(ctx context.Context) (gen int64, tracked bool) {
	err := r.db.QueryRowContext(ctx, `SELECT generation FROM sync_admin_version WHERE id = 1`).Scan(&gen)
	if err != nil {
		if err != sql.ErrNoRows {
			logging.L().Warnf("sync admin: read sync_admin_version failed, falling back to a full scan: %v", err)
		}
		return 0, false
	}
	return gen, true
}

func (b AdminBundle) shallowCopy() AdminBundle {
	out := AdminBundle{Tables: make(map[string][]map[string]any, len(b.Tables))}
	for k, v := range b.Tables {
		out.Tables[k] = v
	}
	return out
}

// scanAdmin is the full read DumpAdmin caches: every synced table, in
// adminTables order, per-till/plugin-local rows filtered and skip/redact
// columns dropped.
func (r *SyncAdminRepo) scanAdmin(ctx context.Context) (AdminBundle, error) {
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
		if t.name == "plugin_storage" {
			// ut-docs#1670: only FiscalRegisterDEPluginID's fiscal_register:
			// namespace syncs — every other plugin's private KV state must
			// never leave this till at all, and neither must a DIFFERENT
			// plugin's row that happens to share the same key prefix.
			kept := recs[:0]
			for _, rec := range recs {
				if fmt.Sprint(rec["plugin_id"]) == FiscalRegisterDEPluginID &&
					strings.HasPrefix(fmt.Sprint(rec["key"]), FiscalRegisterDEKeyPrefix) {
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

	// ut-docs#1589 (found in #1554's own review): roles/permission_actions
	// are migration-seeded catalog tables nothing in this codebase ever
	// DELETEs (additive-only) — verified by grep, and true today because no
	// custom-roles feature exists yet (the day one does, this assumption
	// needs re-checking; see the "future custom-roles feature" note on
	// adminTables above). A replica one release ahead of its primary has
	// extra role/action rows the primary's dump doesn't mention at all, and
	// unconditionally pruning them was silently destroying that skew state
	// with no warning — so both are exempted from deleteMissing entirely,
	// the same shape as settings' own exemption below.
	//
	// role_permissions needs a narrower rule, not the same blanket skip: it
	// DOES have a real, frequent same-version write path — a manager
	// revoking or granting a permission from the matrix editor
	// (AuthRepo.SetRolePermission) — and #1554 depends on the prune phase to
	// heal a *locally fabricated* grant (a row a satellite wrote for a
	// role/action the primary already knows about, but has no matching row
	// for): blanket-exempting role_permissions the way settings is exempted
	// would let that fabricated grant survive forever, silently more
	// permissive than the primary — the opposite of #1554's fail-closed
	// intent, and a real regression a first draft of this fix introduced
	// (caught in this fix's own review). A *revoked* grant is safe either
	// way: SetRolePermission always upserts (INSERT ... ON CONFLICT DO
	// UPDATE SET granted = excluded.granted), so a revoke sets granted=0 on
	// an existing row rather than deleting it — that row stays in the
	// primary's dump and phase 2 below overwrites it with the primary's
	// value regardless of what phase 1 does. So the only rows that need
	// protecting here are ones referencing a role or action the primary's
	// CURRENT dump doesn't know about at all — real version skew, not
	// drift — and those are safe to protect without weakening #1554,
	// because the primary literally cannot have an opinion on a grant for a
	// role/action it doesn't know exists yet.
	knownRoles := map[string]bool{}
	for _, rec := range bundle.Tables["roles"] {
		knownRoles[fmt.Sprint(rec["role"])] = true
	}
	knownActions := map[string]bool{}
	for _, rec := range bundle.Tables["permission_actions"] {
		knownActions[fmt.Sprint(rec["action"])] = true
	}
	rolePermissionSkew := func(rec map[string]any) bool {
		return !knownRoles[fmt.Sprint(rec["role"])] || !knownActions[fmt.Sprint(rec["action"])]
	}

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
		// plugin_storage does its own scoped replace in
		// applyFiscalRegisterStorage (ut-docs#1670) for the same reason —
		// the bundle only ever carries fiscal_register: rows, so the generic
		// prune would wipe every OTHER plugin's private storage on this till.
		if t.name == "settings" || t.name == "plugin_settings" ||
			t.name == "plugin_storage" ||
			t.name == "roles" || t.name == "permission_actions" {
			continue
		}
		var skipPrune func(map[string]any) bool
		if t.name == "role_permissions" {
			skipPrune = rolePermissionSkew
		}
		if err := deleteMissing(ctx, tx, t, recs, skipPrune); err != nil {
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
		if t.name == "plugin_storage" {
			if err := applyFiscalRegisterStorage(ctx, tx, t, recs); err != nil {
				return err
			}
			continue
		}
		cols, err := tableColumns(ctx, tx, t.name)
		if err != nil {
			return err
		}
		// ut-docs#1369: upsertRows batches this table's rows into chunked
		// multi-row statements instead of one exec per row — the two
		// per-row adjustments below (settings/country_settings) still run
		// first, over the same recs slice, exactly as before; only the
		// final write is batched.
		toApply := recs
		if t.name == "settings" || t.name == "country_settings" {
			toApply = make([]map[string]any, 0, len(recs))
			for _, rec := range recs {
				if t.name == "settings" && perTillSetting(fmt.Sprint(rec["key"])) {
					continue // defense in depth: never let a primary write per-till keys
				}
				if t.name == "country_settings" {
					// ut-docs#1669: upsertRows below writes archive_min_days
					// raw, bypassing CountrySettingsRepo.Upsert()'s own
					// ADR-0040 floor check entirely — clamp it here so a
					// rolled-back or buggy primary can never push a
					// satellite below the retention floor via sync (defense
					// in depth, same shape as the per-till-setting skip just
					// above). Only touch it when the bundle actually carries
					// the column — leave a genuinely absent column (an older
					// primary's schema) alone, same as resolveUpsertRow's
					// own "column the primary doesn't know" case.
					if v, ok := rec["archive_min_days"]; ok && syncedDays(v) < GlobalArchiveMinDays {
						rec["archive_min_days"] = GlobalArchiveMinDays
					}
				}
				toApply = append(toApply, rec)
			}
		}
		if err := upsertRows(ctx, tx, t, cols, toApply); err != nil {
			return fmt.Errorf("apply %s: %w", t.name, err)
		}
	}

	if err := backfillCodelessSyncedVariants(ctx, tx); err != nil {
		return fmt.Errorf("backfill codeless synced variants: %w", err)
	}
	if err := invalidateStalePriceHistoryOnSync(ctx, tx); err != nil {
		return fmt.Errorf("invalidate stale price_history: %w", err)
	}

	return tx.Commit()
}

// invalidateStalePriceHistoryOnSync is ADR-0099 Decision 2 (ut-docs#2348,
// resolving ut-docs#1671): price_history stays out of adminTables — it is
// an ever-growing ledger of every price change a shop ever made, and
// dumping it whole on every poll (the only mechanism DumpAdmin/ApplyAdmin
// have) has no natural ceiling, unlike every current-state table in that
// list. A satellite doesn't need the history anyway; it only needs to
// never keep trusting a stale open override once the primary has moved
// the underlying price — which is exactly what happens otherwise, because
// POSRepo.ResolveCurrentPrice / lookupPriceHistory prefer an open
// price_history row (ends_at IS NULL) over the freshly-synced
// items.base_price / item_variants.price, and nothing ever revisits that
// row. So instead of syncing the table itself, every admin-bundle apply
// closes any locally-open price_history row for an item/variant this
// bundle just synced. DumpAdmin sends the FULL items/item_variants tables
// (not incremental) whenever it sends them at all, so for a complete
// bundle "every id now in items/item_variants" IS the bundle's contents —
// there is no narrower per-id targeting to preserve. (A bundle from an
// older primary that omits one of these tables entirely just means Phase
// 1/2 skip it above; this step still closes every open row against
// whatever items/item_variants already hold locally, which is the safe
// direction — it can only ever remove a stale override, never introduce
// one.)
//
// Deliberately closes ANY open row, not only a currently-active one
// (starts_at <= now) — a future-dated open row would otherwise activate
// later with no sync guaranteed to run at that moment, reopening the exact
// divergence this exists to close (an earlier draft of the ADR scoped it
// to active rows only and was corrected on review). A row that already
// closed (ends_at in the past) is inert to every reader and is left alone.
// CURRENT_TIMESTAMP here is the satellite's own clock, consistent with how
// starts_at/ends_at are already compared everywhere else on this table.
//
// Same precedent as backfillCodelessSyncedVariants just above: runs
// unconditionally at the end of every ApplyAdmin, same transaction,
// set-based (two statements, no per-row loop). It does NOT close the
// cloud-directive write path — a satellite can still receive an ungated
// SetPrice directive (internal/pages/cloudsync_wire.go, ut-docs#2353) and
// write its own row directly; this only cleans that up on the next
// admin bundle that actually changes (sync_admin.go's `!Unchanged`
// check gates whether ApplyAdmin runs at all) — an unbounded window on a
// steady-state shop whose admin state never moves again, not literally
// "the next poll".
func invalidateStalePriceHistoryOnSync(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `
UPDATE price_history SET ends_at = CURRENT_TIMESTAMP
 WHERE ends_at IS NULL AND item_id IN (SELECT id FROM items)`); err != nil {
		return fmt.Errorf("invalidate stale item price_history: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE price_history SET ends_at = CURRENT_TIMESTAMP
 WHERE ends_at IS NULL AND variant_id IN (SELECT id FROM item_variants)`); err != nil {
		return fmt.Errorf("invalidate stale variant price_history: %w", err)
	}
	return nil
}

// backfillCodelessSyncedVariants is ut-docs#2230's defense against the
// second variant-write path found while fixing that card: execUpsertBatch
// (upsertRows' own writer, above) is a fully generic column-copy engine
// shared by every table in adminTables, and — like the country_settings
// special-case just above already concedes for ADR-0040's floor — it
// bypasses every table's own domain rules, sku generation included. A
// primary that is itself still behind on ut-docs#1900 or on this exact fix
// (028_backfill_codeless_variant_skus.sql, applied locally on every till
// including the primary at its own next boot) can hand a satellite a
// genuinely codeless item_variants row this way — active, no barcode, no
// sku — even though the satellite's own local copy of 028 already ran once
// at boot and has nothing left of its own to catch. Same failure class as
// ut-docs#1459: generate-and-surface, not hide.
//
// Runs unconditionally at the end of every ApplyAdmin (not only when the
// bundle happens to mention item_variants) and is a single indexed-friendly
// scan when there is nothing to do. Mirrors 028's own scope and SKU
// convention exactly (active, no barcode, blank/NULL sku;
// generatedVariantSKU(), same "VAR-" + 8 uppercase hex chars) so a variant
// fixed here is indistinguishable from one the migration or CreateVariant
// itself fixed — and reuses generatedVariantSKU() directly since this file
// lives in the same package.
//
// Deliberately NOT sticky across repeated polls: if the primary is still
// sending a blank sku on the NEXT pull, the generic upsert above rewrites
// sku=NULL first (primary always wins, this table's whole raison d'être),
// and this function then hands out a *fresh* code again. The value can
// therefore change between polls while a primary stays behind — cosmetic,
// self-resolving the moment that primary itself boots the fix — but it can
// never go back to being genuinely codeless, which is the actual
// correctness property this guards.
//
// Also catches a revived retire-mangled sku (ut-docs#2273): deleteMissing's
// FK-blocked retire-in-place rewrites item_variants.sku to "<sku>~<id>" — a
// non-blank value — on retire, and stripRetireMangle (which undoes that same
// mangle for every OTHER reader) is never applied here. Without this extra
// clause, ut-docs#2246's sticky COALESCE on this column would treat a
// revived row's local mangled sku as "real" and freeze it in place forever
// once a version-skewed primary sends a blank sku for the same id, instead
// of this function generating a fresh, real-looking one the same way it
// already does for a genuinely blank sku. The `v.sku LIKE '%~' || v.id`
// match mirrors the exact suffix shape the retire-in-place CASE above
// produces. Same ambiguity stripRetireMangle's own doc comment already
// accepts (a real value that happens to end in "~"+its own id is
// indistinguishable from a mangle) — here that risk is a persisted
// overwrite rather than a display-only misread, but item_variants.id is
// always a generated uuid.NewString(), never user-chosen, so a real sku
// coinciding with "~"+its own row's UUID is not a reachable case.
func backfillCodelessSyncedVariants(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `
SELECT v.id
FROM item_variants v
WHERE v.is_active = 1
  AND (v.sku IS NULL OR TRIM(v.sku) = '' OR v.sku LIKE '%~' || v.id)
  AND NOT EXISTS (SELECT 1 FROM variant_barcodes b WHERE b.variant_id = v.id)`)
	if err != nil {
		return fmt.Errorf("find codeless synced variants: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("scan codeless synced variant id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()

	for _, id := range ids {
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			_, err = tx.ExecContext(ctx, `UPDATE item_variants SET sku = ? WHERE id = ?`, generatedVariantSKU(), id)
			if err == nil || !isUniqueViolation(err) {
				break
			}
		}
		if err != nil {
			return fmt.Errorf("assign sku to synced variant %s: %w", id, err)
		}
	}
	return nil
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
	// ut-docs#1369 review (finding 1): batch this loop's writes the same way
	// ApplyAdmin's generic phase-2 path does, instead of one exec per row
	// per row — a shop with many installed plugins each carrying many
	// global settings keys hits this path on every admin pull exactly like
	// the generic tables the card was filed against.
	toApply := make([]map[string]any, 0, len(recs))
	for _, rec := range recs {
		if !installed[fmt.Sprint(rec["plugin_id"])] {
			continue
		}
		if fmt.Sprint(rec["scope"]) != "global" {
			continue // defense in depth: a primary must never write per-till scopes
		}
		toApply = append(toApply, rec)
	}
	if err := upsertRows(ctx, tx, t, cols, toApply); err != nil {
		return fmt.Errorf("apply plugin_settings: %w", err)
	}
	return nil
}

// applyFiscalRegisterStorage replaces this till's fiscal_register:-prefixed
// plugin_storage rows — for FiscalRegisterDEPluginID specifically — with the
// primary's copy (ut-docs#1670). Delete-then-insert, scoped by BOTH
// plugin_id and key prefix — never touches a row under any other plugin_id,
// or any other key, for any plugin: the DELETE's WHERE clause is the only
// thing standing between this and wiping another plugin's private storage
// (or broadcasting it to every satellite via the dump side), so it must
// never be loosened to match on prefix alone or anything table-wide.
// FiscalRegisterDEKeyPrefix ("fiscal_register:") is a fixed compile-time
// constant with no '%'/'_' characters, so the LIKE pattern below needs no
// ESCAPE clause. Same shape as applyPluginSettings just above: a scoped
// delete-then-insert stands in for deleteMissing/upsertRows because the
// generic path can't safely reason about a bundle that is deliberately a
// subset of the table.
func applyFiscalRegisterStorage(ctx context.Context, tx *sql.Tx, t adminTable, recs []map[string]any) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM plugin_storage WHERE plugin_id = ? AND key LIKE ?`,
		FiscalRegisterDEPluginID, FiscalRegisterDEKeyPrefix+"%"); err != nil {
		return fmt.Errorf("apply plugin_storage (fiscal register): %w", err)
	}
	cols, err := tableColumns(ctx, tx, t.name)
	if err != nil {
		return err
	}
	// ut-docs#1369 review (finding 1): this was the sharpest un-batched
	// survivor — a blanket DELETE followed by one INSERT per surviving row,
	// on every admin pull, over an unbounded fiscal-register keyspace. Same
	// batching swap as applyPluginSettings just above.
	toApply := make([]map[string]any, 0, len(recs))
	for _, rec := range recs {
		if fmt.Sprint(rec["plugin_id"]) != FiscalRegisterDEPluginID ||
			!strings.HasPrefix(fmt.Sprint(rec["key"]), FiscalRegisterDEKeyPrefix) {
			continue // defense in depth: mirrors applyPluginSettings' own scope re-check
		}
		toApply = append(toApply, rec)
	}
	if err := upsertRows(ctx, tx, t, cols, toApply); err != nil {
		return fmt.Errorf("apply plugin_storage (fiscal register): %w", err)
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

// divergencePruneTables names every adminTable that made the same
// transition: UNSYNCED (so a satellite could freely create its own local
// row) to synced-and-gated-primary-only. A shop that already had a
// satellite-created row from before its fix will have it pruned on its very
// first post-upgrade pull — see adminTables' own top comment for the full
// trace, and logSatelliteDivergencePrune below. Every OTHER adminTable
// prunes routinely (that's what sync is for), so this map is deliberately
// scoped to just the tables that made this exact transition: a warning here
// signals pre-existing divergence worth a shop owner's attention, not
// everyday sync noise.
var divergencePruneTables = map[string]string{
	"registers":             "ut-docs#1590",
	"stock_locations":       "ut-docs#1590",
	"item_modifier_groups":  "ut-docs#1667",
	"item_modifier_options": "ut-docs#1667",
}

// logSatelliteDivergencePrune notes when deleteMissing successfully prunes
// (hard-deletes or retires in place) a row from one of divergencePruneTables.
// INFO, not WARN (ut-docs#2798): the prune is the designed clean-up working,
// not a problem — a WARN lands in the cloud's "Attention needed" list.
func logSatelliteDivergencePrune(t adminTable, args []any, action string) {
	card, ok := divergencePruneTables[t.name]
	if !ok {
		return
	}
	logging.L().Infof("sync pull: pruned pre-existing satellite-local %s row %v (%s) — see %s: a row created directly on a satellite before that fix is expected to disappear on the first sync after upgrading", t.name, args, action, card)
}

// deleteMissing prunes rows this table's PK set no longer includes in the
// bundle. skipPrune, if non-nil, is consulted for each row that would
// otherwise be pruned and additionally protects it when it returns true —
// role_permissions uses this (ut-docs#1589) to distinguish "the primary's
// current dump doesn't know this row's role/action at all" (version skew;
// must NOT prune) from "the primary knows both but has no matching grant
// row" (real same-version drift; must still prune, or a satellite-local
// grant survives forever — see ApplyAdmin's own comment for why this
// distinction matters).
func deleteMissing(ctx context.Context, tx *sql.Tx, t adminTable, recs []map[string]any, skipPrune func(rec map[string]any) bool) error {
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
		if skipPrune != nil && skipPrune(rec) {
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
			logSatelliteDivergencePrune(t, args, "hard-deleted, no history")
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
				logSatelliteDivergencePrune(t, args, "retired in place, has history")
				continue
			}
		}
		logging.L().Warnf("sync pull: cannot prune %s %v (kept): %v", t.name, args, err)
	}
	return nil
}

// stripRetireMangle undoes deleteMissing's uniqueness-freeing "<name>~<id>"
// suffix on an FK-blocked retire-in-place, so a mangled DB value never
// reaches a display path (ut-docs#1610). Six admin tables use the SAME
// column for identity-uniqueness and display (brands.name, tax_codes.name,
// payment_methods.name, users.username, stock_locations.name,
// registers.name); every repository reader that surfaces one of those to
// staff — including the admin listings that deliberately show retired rows
// — runs its scanned value through this before returning it.
//
// "Every reader" includes the ones that reach the column through a JOIN
// rather than a direct SELECT, which is where the first pass at this fix
// missed four (review of ut-docs#1610): ListStockLevels, GetLowStockItems,
// variantStockForExport and ListRegisterLocations all join a name in
// without an is_active filter. For stock_locations that is in fact the
// MOST likely place the mangle surfaces — a location whose prune is
// FK-blocked is blocked precisely because inventory rows reference it, and
// those are the rows those queries return. When adding a reader, the test
// is "does this value reach a person?", not "does this query name the
// table in its FROM clause?".
//
// Only strips when name ends in exactly "~"+id: an active row (or a table
// that's never mangled) is returned unchanged, even one whose real name
// contains a literal '~' or ends in some OTHER row's id. Strips exactly
// once — deleteMissing's CASE keeps the mangle idempotent across pulls, so
// a doubled suffix never exists to begin with.
func stripRetireMangle(id, name string) string {
	if id == "" {
		return name
	}
	return strings.TrimSuffix(name, "~"+id)
}

// syncedDays converts a bundle value's dynamic type to int64: int64 when
// ApplyAdmin is called directly in-process (scanGeneric's own type for an
// INTEGER column), float64 after a real wire hop (wireTrip/JSON turns every
// number into float64). Same defensive-conversion shape as bkpScanInt in
// bkp_products_repo.go, for the same reason — never assume a single Go type
// for a value that traveled through JSON.
func syncedDays(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	case int:
		return int64(n)
	default:
		return 0
	}
}

// resolvedUpsertRow is one row's fully-resolved upsert shape: which columns
// actually travel (skip/redact/missing-column rules already applied, ut-docs#1369's
// upsertRows groups rows sharing an identical `names`+`sets` signature so they
// can share one multi-row statement), the SET-clause fragments for an
// ON CONFLICT UPDATE, and the row's own bind values in `names` order.
type resolvedUpsertRow struct {
	names []string
	sets  []string
	args  []any
}

// resolveUpsertRow applies upsertRows' (and, historically, the now-removed
// single-row upsertRow's) shared per-row column rules:
// a skipCols entry never travels even if the bundle carries it; a redactCols
// entry always force-writes NULL, never the bundle's value; any other column
// travels only if the bundle's row actually has it (an older/newer primary's
// schema may not). Returns a nil names slice when nothing survives — the
// caller skips the row entirely — a no-op, same as the pre-#1369 single-row
// upsertRow did for an empty column set.
func resolveUpsertRow(t adminTable, cols []string, rec map[string]any) resolvedUpsertRow {
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
	sticky := map[string]bool{}
	for _, c := range t.stickyNonBlankCols {
		sticky[c] = true
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
			if sticky[c] {
				// A blank incoming value leaves the existing local value
				// untouched; a real one still overwrites — see
				// stickyNonBlankCols' own doc comment.
				sets = append(sets, c+" = COALESCE(NULLIF(excluded."+c+", ''), "+c+")")
			} else {
				sets = append(sets, c+" = excluded."+c)
			}
		}
	}
	return resolvedUpsertRow{names: names, sets: sets, args: args}
}

// maxBatchPlaceholders bounds how many bound parameters one multi-row
// upsertRows statement carries (rows-in-chunk * columns-in-signature).
// ut-docs#1369 review (finding 3): measured directly against this repo's
// actual driver (modernc.org/sqlite v1.58.0) rather than assumed — a single
// statement with 32,764 bound parameters succeeds; 40,000 fails with "too
// many SQL variables" (SQLite's SQLITE_MAX_VARIABLE_NUMBER, 32766 by
// default on a modern build). 4000 leaves a wide safety margin below that
// measured ceiling — comfortably clear even if a future driver/SQLite
// build ships a much lower compile-time limit than today's default — while
// still capturing most of the real win: the widest adminTable (`items`,
// 19 columns) chunks at ~210 rows instead of the historical 1 (or an
// overly-conservative earlier draft's 26), so a large catalog needs
// single-digit statements per pull instead of one per row. The
// divide-and-floor-at-1 below just means a pathologically wide table
// (500+ columns; none exists today) still works, one row at a time,
// instead of erroring.
const maxBatchPlaceholders = 4000

// upsertBatch is one multi-row statement's worth of identically-shaped
// rows — the unit buildUpsertBatches groups recs into and upsertRows then
// executes one-for-one.
type upsertBatch struct {
	names []string
	sets  []string
	rows  [][]any
}

// buildUpsertBatches groups recs by resolveUpsertRow's exact column
// signature rather than assuming uniformity: within one bundle every row of
// a table normally resolves identically (one scan of one schema), but a
// rolling upgrade can hand ApplyAdmin a primary whose dump has a different
// column set for the same table, and grouping preserves each row's original
// per-row semantics exactly — only the number of statements changes, not
// what any single row writes. Within one signature's group, row order
// follows first-appearance in recs; across DIFFERENT signatures, groups are
// emitted in first-seen-signature order, which does not generally preserve
// recs' original interleaving (a mixed-signature bundle can apply its rows
// out of original order across groups) — harmless in the normal uniform
// case, and only reachable at all during a rolling upgrade; see
// TestBuildUpsertBatches_DifferingColumnSetsGetSeparateGroups.
//
// Splits each group into maxBatchPlaceholders-safe chunks. Pure and DB-free
// by design, so the grouping/chunking behaviour is unit-testable without a
// database.
func buildUpsertBatches(t adminTable, cols []string, recs []map[string]any) []upsertBatch {
	type group struct {
		names []string
		sets  []string
		rows  [][]any
	}
	groups := map[string]*group{}
	var order []string
	for _, rec := range recs {
		rr := resolveUpsertRow(t, cols, rec)
		if len(rr.names) == 0 {
			continue
		}
		sig := strings.Join(rr.names, "\x1f")
		g, ok := groups[sig]
		if !ok {
			g = &group{names: rr.names, sets: rr.sets}
			groups[sig] = g
			order = append(order, sig)
		}
		g.rows = append(g.rows, rr.args)
	}

	var batches []upsertBatch
	for _, sig := range order {
		g := groups[sig]
		chunkRows := maxBatchPlaceholders / len(g.names)
		if chunkRows < 1 {
			chunkRows = 1
		}
		for start := 0; start < len(g.rows); start += chunkRows {
			end := start + chunkRows
			if end > len(g.rows) {
				end = len(g.rows)
			}
			batches = append(batches, upsertBatch{names: g.names, sets: g.sets, rows: g.rows[start:end]})
		}
	}
	return batches
}

// upsertRows is ApplyAdmin's phase-2 batching path (ut-docs#1369): before
// this, every row of every synced table was its own INSERT ... ON CONFLICT
// statement, so a bundle with tens of thousands of byte-identical rows (one
// unrelated price edit bumps the whole bundle's generation, ut-docs#1368)
// replayed that many round trips on every replica's poll. DumpAdmin's own
// change-marker (ut-docs#1368) is a coarse, whole-bundle generation counter,
// not a per-row change signal, so there is no cheap way to skip re-applying
// an unchanged row — this instead cuts the cost of applying every row by
// batching same-shaped rows into chunked multi-row statements (via
// buildUpsertBatches, above), without changing what gets written or the
// wire/bundle format.
func upsertRows(ctx context.Context, tx *sql.Tx, t adminTable, cols []string, recs []map[string]any) error {
	for _, b := range buildUpsertBatches(t, cols, recs) {
		if err := execUpsertBatch(ctx, tx, t, b.names, b.sets, b.rows); err != nil {
			return err
		}
	}
	return nil
}

// execUpsertBatch issues one multi-row INSERT ... ON CONFLICT statement for
// rows that all share the same resolved names/sets (see upsertRows).
func execUpsertBatch(ctx context.Context, tx *sql.Tx, t adminTable, names, sets []string, rows [][]any) error {
	rowPH := "(" + strings.TrimSuffix(strings.Repeat("?, ", len(names)), ", ") + ")"
	placeholders := make([]string, len(rows))
	args := make([]any, 0, len(rows)*len(names))
	for i, row := range rows {
		placeholders[i] = rowPH
		args = append(args, row...)
	}
	q := `INSERT INTO ` + t.name + ` (` + strings.Join(names, ", ") + `) VALUES ` +
		strings.Join(placeholders, ", ") + ` ON CONFLICT (` + strings.Join(t.pk, ", ") + `) DO `
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

// SettingScopeKind is where a settings key belongs (ut-docs#2791): to one
// till, to the whole shop, or -- a key nobody classified yet -- neither.
type SettingScopeKind int

const (
	// SettingUnclassified is a key in neither list. The main till refuses
	// it on POST /api/sync/settings/apply, and
	// TestSettingScope_EveryUsedKeyIsClassified fails on any key the code
	// uses that lands here.
	SettingUnclassified SettingScopeKind = iota
	// SettingPerTill is a PerTillSettingPrefixes key: saved on the till it
	// was changed on, never synced.
	SettingPerTill
	// SettingShopWide is a ShopWideSettingPrefixes key: the same for every
	// till of the shop, carried main -> additional tills by the admin
	// bundle. An additional till writes it through to the main till
	// (pages/settings_sync_proxy.go) rather than locally, where the next
	// admin pull would silently overwrite it.
	SettingShopWide
)

// ShopWideSettingPrefixes are the settings key families that are the same
// for every till of one shop (ut-docs#2791). Like PerTillSettingPrefixes, an
// entry is a prefix; a full key works as an exact match. It does NOT change
// what the admin bundle carries -- DumpAdmin/ApplyAdmin still sync every key
// that is not per-till -- it only names, for the write-through, which keys
// an additional till must send to its main till. PerTillSettingPrefixes
// wins where both match (reports.eod_* under reports.,
// fiscal.pending_sign_retries under fiscal.).
var ShopWideSettingPrefixes = []string{
	// Shop identity, money, tax, locale and trading rules.
	"store.", "shop.", "sale.", "pos.", "payments.", "invoice.", "receipt.",
	// Fiscal posture and override state (fiscal.pending_sign_retries is
	// per-till, above).
	"fiscal.",
	// Kiosk behaviour, the idle-lock policy, shop-wide report options
	// (reports.eod_* is per-till), auto-update schedule, setup state.
	"kiosk.", "auth.", "reports.", "update.", "setup.",
	// Barcode handling (data/barcode_settings.go).
	BarcodeEnabledSymbologiesKey, CatalogImportBarcodeFromSKUDefaultKey,
	// The store-level marketplace keys (see PerTillSettingPrefixes' own
	// comment). Listed key by key: a new marketplace.* key must be
	// classified on purpose, since most of that family is per-till.
	"marketplace.store_id", "marketplace.merchant_id",
	"marketplace.telemetry_opt_in", "marketplace.auto_register_opt_in",
	// Scope genuinely unclear: these read like one till's own state, but
	// every one of them is carried by the admin bundle today (none is
	// per-till), so they are shop-wide by the rule ut-docs#2791 set --
	// shop-wide iff synced today. Moving any of them to per-till is
	// ut-docs#2950 (it changes what the admin bundle syncs).
	"till.name",      // the main till's name; a replica's own name is sync.till_name
	"menu.",          // menu.restored_keys
	"diagnostics.",   // ADR-0092 support-session rows
	"cloudsync.",     // cloud snapshot / order-tracking hashes
	"install.",       // install.desktop_kiosk_overlay_provisioned
	"lan_discovery.", // lan_discovery.till_id
}

// SettingScope classifies a settings key. Per-till wins over shop-wide.
func SettingScope(key string) SettingScopeKind {
	if key == "" {
		return SettingUnclassified
	}
	if perTillSetting(key) {
		return SettingPerTill
	}
	for _, p := range ShopWideSettingPrefixes {
		if strings.HasPrefix(key, p) {
			return SettingShopWide
		}
	}
	return SettingUnclassified
}
