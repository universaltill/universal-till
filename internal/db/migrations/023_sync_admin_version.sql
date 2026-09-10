-- ut-docs#1368: a one-row generation counter that moves whenever any table
-- in the LAN admin-sync bundle changes, so GET /api/sync/admin can answer an
-- unchanged replica poll from an in-process cache (internal/data's
-- SyncAdminRepo.DumpAdmin) with ONE single-row SELECT instead of the full
-- 34-table scan + JSON marshal + SHA-256 it used to run on every poll from
-- every replica (~30s apart, per till), before the ?have= fingerprint check
-- could short-circuit anything. The wire format and the ?have= contract are
-- unchanged; this is purely server-side work avoidance.
--
-- One AFTER INSERT / AFTER UPDATE / AFTER DELETE trigger per table in
-- sync_admin_repo.go's adminTables (SQLite takes one event per CREATE
-- TRIGGER — they cannot be combined). The set is pinned by
-- TestSyncAdminVersion_EveryAdminTableHasTriggers: a table added to
-- adminTables later needs its own three triggers in a NEW migration (this
-- file is append-only once applied, ADR-0074), or the cache would never see
-- that table change.
--
-- Deliberately imprecise for every table but tills: a per-till-scoped
-- settings / plugin_settings / plugin_storage row (which DumpAdmin filters
-- OUT of the bundle) still bumps the counter. That costs one unnecessary
-- rescan whose result is byte-identical to the cached one — never a stale
-- or wrong bundle — so it is not worth a WHEN clause per filtered prefix.
--
-- tills is the exception and MUST stay precise: TillsRepo.TillByBearerHash
-- writes tills.last_seen_at on EVERY authenticated sync call, this very
-- endpoint included, so an unconditional UPDATE trigger would bump the
-- counter on every poll and the cache would never hit (the whole
-- optimisation silently defeated, with nothing failing). last_seen_at and
-- bearer_hash are both redactCols — never part of the bundle — so the
-- UPDATE trigger fires only when name or enrolled_at actually changes (IS,
-- not =, so NULL-safe). Pinned by TestSyncAdminVersion_TillAuthTouchDoesNotBump.
--
-- IF NOT EXISTS / INSERT OR IGNORE throughout: this repo's
-- fiscal_signing_keys_{rename,split}_test.go rewinds the ledger below this
-- version and re-runs every later migration against an already-migrated
-- file (openAtPreMigrationSchema), so everything here must be re-appliable
-- — same reason 021 uses IF NOT EXISTS.
--
-- First migration to carry CREATE TRIGGER ... BEGIN ... END blocks: db.go's
-- splitStatements learnt the construct for this card (triggerBlockOpen);
-- migration 007's header records why it could not before.

CREATE TABLE IF NOT EXISTS sync_admin_version (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    generation INTEGER NOT NULL DEFAULT 0
);

INSERT OR IGNORE INTO sync_admin_version (id, generation) VALUES (1, 0);

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_tax_codes_ins AFTER INSERT ON tax_codes
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_tax_codes_upd AFTER UPDATE ON tax_codes
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_tax_codes_del AFTER DELETE ON tax_codes
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_brands_ins AFTER INSERT ON brands
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_brands_upd AFTER UPDATE ON brands
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_brands_del AFTER DELETE ON brands
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_categories_ins AFTER INSERT ON categories
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_categories_upd AFTER UPDATE ON categories
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_categories_del AFTER DELETE ON categories
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_customers_ins AFTER INSERT ON customers
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_customers_upd AFTER UPDATE ON customers
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_customers_del AFTER DELETE ON customers
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_payment_methods_ins AFTER INSERT ON payment_methods
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_payment_methods_upd AFTER UPDATE ON payment_methods
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_payment_methods_del AFTER DELETE ON payment_methods
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_users_ins AFTER INSERT ON users
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_users_upd AFTER UPDATE ON users
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_users_del AFTER DELETE ON users
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_stock_locations_ins AFTER INSERT ON stock_locations
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_stock_locations_upd AFTER UPDATE ON stock_locations
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_stock_locations_del AFTER DELETE ON stock_locations
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_registers_ins AFTER INSERT ON registers
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_registers_upd AFTER UPDATE ON registers
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_registers_del AFTER DELETE ON registers
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_roles_ins AFTER INSERT ON roles
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_roles_upd AFTER UPDATE ON roles
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_roles_del AFTER DELETE ON roles
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_permission_actions_ins AFTER INSERT ON permission_actions
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_permission_actions_upd AFTER UPDATE ON permission_actions
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_permission_actions_del AFTER DELETE ON permission_actions
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_role_permissions_ins AFTER INSERT ON role_permissions
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_role_permissions_upd AFTER UPDATE ON role_permissions
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_role_permissions_del AFTER DELETE ON role_permissions
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_items_ins AFTER INSERT ON items
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_items_upd AFTER UPDATE ON items
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_items_del AFTER DELETE ON items
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_item_barcodes_ins AFTER INSERT ON item_barcodes
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_item_barcodes_upd AFTER UPDATE ON item_barcodes
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_item_barcodes_del AFTER DELETE ON item_barcodes
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_item_variants_ins AFTER INSERT ON item_variants
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_item_variants_upd AFTER UPDATE ON item_variants
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_item_variants_del AFTER DELETE ON item_variants
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_variant_barcodes_ins AFTER INSERT ON variant_barcodes
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_variant_barcodes_upd AFTER UPDATE ON variant_barcodes
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_variant_barcodes_del AFTER DELETE ON variant_barcodes
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_option_sets_ins AFTER INSERT ON option_sets
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_option_sets_upd AFTER UPDATE ON option_sets
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_option_sets_del AFTER DELETE ON option_sets
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_option_set_values_ins AFTER INSERT ON option_set_values
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_option_set_values_upd AFTER UPDATE ON option_set_values
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_option_set_values_del AFTER DELETE ON option_set_values
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_item_option_sets_ins AFTER INSERT ON item_option_sets
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_item_option_sets_upd AFTER UPDATE ON item_option_sets
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_item_option_sets_del AFTER DELETE ON item_option_sets
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_item_variant_options_ins AFTER INSERT ON item_variant_options
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_item_variant_options_upd AFTER UPDATE ON item_variant_options
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_item_variant_options_del AFTER DELETE ON item_variant_options
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_related_items_ins AFTER INSERT ON related_items
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_related_items_upd AFTER UPDATE ON related_items
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_related_items_del AFTER DELETE ON related_items
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_item_modifier_groups_ins AFTER INSERT ON item_modifier_groups
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_item_modifier_groups_upd AFTER UPDATE ON item_modifier_groups
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_item_modifier_groups_del AFTER DELETE ON item_modifier_groups
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_item_modifier_options_ins AFTER INSERT ON item_modifier_options
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_item_modifier_options_upd AFTER UPDATE ON item_modifier_options
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_item_modifier_options_del AFTER DELETE ON item_modifier_options
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_promotions_ins AFTER INSERT ON promotions
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_promotions_upd AFTER UPDATE ON promotions
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_promotions_del AFTER DELETE ON promotions
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_shortcut_buttons_ins AFTER INSERT ON shortcut_buttons
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_shortcut_buttons_upd AFTER UPDATE ON shortcut_buttons
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_shortcut_buttons_del AFTER DELETE ON shortcut_buttons
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_tables_ins AFTER INSERT ON tables
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_tables_upd AFTER UPDATE ON tables
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_tables_del AFTER DELETE ON tables
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_kitchen_stations_ins AFTER INSERT ON kitchen_stations
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_kitchen_stations_upd AFTER UPDATE ON kitchen_stations
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_kitchen_stations_del AFTER DELETE ON kitchen_stations
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_item_station_routes_ins AFTER INSERT ON item_station_routes
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_item_station_routes_upd AFTER UPDATE ON item_station_routes
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_item_station_routes_del AFTER DELETE ON item_station_routes
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_category_station_routes_ins AFTER INSERT ON category_station_routes
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_category_station_routes_upd AFTER UPDATE ON category_station_routes
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_category_station_routes_del AFTER DELETE ON category_station_routes
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_translation_overrides_ins AFTER INSERT ON translation_overrides
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_translation_overrides_upd AFTER UPDATE ON translation_overrides
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_translation_overrides_del AFTER DELETE ON translation_overrides
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_settings_ins AFTER INSERT ON settings
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_settings_upd AFTER UPDATE ON settings
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_settings_del AFTER DELETE ON settings
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_country_settings_ins AFTER INSERT ON country_settings
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_country_settings_upd AFTER UPDATE ON country_settings
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_country_settings_del AFTER DELETE ON country_settings
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_plugin_settings_ins AFTER INSERT ON plugin_settings
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_plugin_settings_upd AFTER UPDATE ON plugin_settings
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_plugin_settings_del AFTER DELETE ON plugin_settings
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_plugin_storage_ins AFTER INSERT ON plugin_storage
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_plugin_storage_upd AFTER UPDATE ON plugin_storage
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_plugin_storage_del AFTER DELETE ON plugin_storage
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

-- tills: see the header — UPDATE fires only on the columns that travel.
CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_tills_ins AFTER INSERT ON tills
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_tills_upd AFTER UPDATE ON tills
WHEN NOT (OLD.name IS NEW.name AND OLD.enrolled_at IS NEW.enrolled_at)
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS trg_sync_admin_version_tills_del AFTER DELETE ON tills
BEGIN
  UPDATE sync_admin_version SET generation = generation + 1 WHERE id = 1;
END;
