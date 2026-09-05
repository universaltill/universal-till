-- 001_init.sql — Universal Till baseline schema and seed rows.
--
-- Generated 2026-09-02 by a one-off dump from a fresh Open() against the
-- previous 78-migration ledger (ADR-0074 Decision 2, ut-docs#1425). Schema
-- and seed-row equivalence between that ledger and this file was proven by
-- a test run once before the old files were deleted; its recorded output is
-- in the ut-docs#1425 review record.
--
-- This file may be edited freely until the first paying shop goes live on
-- it (ADR-0074 Decision 1); it is append-only from that point on.
--
-- Conventions: schema_migrations (the applied-migration ledger) is created
-- and populated by internal/db/db.go, never by a migration file. Every
-- other table below is listed in creation order as SQLite recorded it —
-- this is NOT a strict FK-dependency order (SQLite resolves FOREIGN KEY
-- targets at DML time, not CREATE TABLE time, so a handful of tables here
-- reference one defined later, e.g. promotions.customer_id -> customers,
-- several tables' table_id -> tables; harmless, confirmed by a clean fresh
-- Open, but don't assume forward references can't happen when editing).

-- ----------------------------------------
-- Schema
-- ----------------------------------------
CREATE TABLE settings (
    key         TEXT PRIMARY KEY,
    value       TEXT NOT NULL,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE promotions (
    code        TEXT PRIMARY KEY,                
    type        TEXT NOT NULL DEFAULT 'amount'   CHECK (type IN ('amount','percent')),
    value       INTEGER NOT NULL,                
    description TEXT,
    starts_at   TEXT,
    ends_at     TEXT,
    customer_id TEXT,                            
    is_active   INTEGER NOT NULL DEFAULT 1, is_sample_data INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (customer_id) REFERENCES customers (id)
);

CREATE TABLE tax_codes (
    id                  TEXT PRIMARY KEY,
    name                TEXT NOT NULL UNIQUE,         
    rate_basis_points   INTEGER NOT NULL,            
    is_active           INTEGER NOT NULL DEFAULT 1
, takeaway_rate_basis_points INTEGER);

CREATE TABLE categories (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    parent_id   TEXT,
    sort_order  INTEGER NOT NULL DEFAULT 0, color TEXT,
    FOREIGN KEY (parent_id) REFERENCES categories (id)
);

CREATE INDEX idx_categories_parent ON categories (parent_id);

CREATE TABLE brands (
    id      TEXT PRIMARY KEY,
    name    TEXT NOT NULL UNIQUE
);

CREATE TABLE stock_locations (
    id      TEXT PRIMARY KEY,
    name    TEXT NOT NULL UNIQUE
, is_active INTEGER NOT NULL DEFAULT 1, address_street TEXT, address_postcode TEXT, address_city TEXT);

CREATE TABLE customers (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    phone       TEXT,
    email       TEXT,
    address     TEXT,
    loyalty_no  TEXT UNIQUE,
    created_at  TEXT NOT NULL DEFAULT (datetime('now'))
, is_sample_data INTEGER NOT NULL DEFAULT 0);

CREATE INDEX idx_customers_phone ON customers (phone);

CREATE TABLE registers (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,        
    location_id TEXT,
    is_active   INTEGER NOT NULL DEFAULT 1,
    FOREIGN KEY (location_id) REFERENCES stock_locations (id)
);

CREATE TABLE users (
    id          TEXT PRIMARY KEY,
    username    TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    role        TEXT NOT NULL DEFAULT 'cashier', 
    pin_hash    TEXT,
    is_active   INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE payment_methods (
    id          TEXT PRIMARY KEY,            
    name        TEXT NOT NULL UNIQUE,
    type        TEXT NOT NULL,              
    is_active   INTEGER NOT NULL DEFAULT 1,
    sort_order  INTEGER NOT NULL DEFAULT 0
, plugin_id TEXT);

CREATE TABLE items (
    id              TEXT PRIMARY KEY,           
    sku             TEXT UNIQUE,                
    name            TEXT NOT NULL,
    description     TEXT,
    category_id     TEXT,
    brand_id        TEXT,
    unit            TEXT NOT NULL DEFAULT 'each',  
    base_price      INTEGER NOT NULL,           
    cost_price      INTEGER,                    
    tax_code_id     TEXT,                       
    reorder_level   INTEGER NOT NULL DEFAULT 0, 
    is_active       INTEGER NOT NULL DEFAULT 1,
    is_weighed      INTEGER NOT NULL DEFAULT 0,
    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at      TEXT NOT NULL DEFAULT (datetime('now')), lead_time_days INTEGER NOT NULL DEFAULT 0, is_sample_data INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (category_id) REFERENCES categories (id),
    FOREIGN KEY (brand_id)    REFERENCES brands (id),
    FOREIGN KEY (tax_code_id) REFERENCES tax_codes (id)
);

CREATE INDEX idx_items_category ON items (category_id);

CREATE INDEX idx_items_active   ON items (is_active);

CREATE TABLE item_barcodes (
    barcode       TEXT PRIMARY KEY,
    item_id       TEXT NOT NULL,
    barcode_type  TEXT DEFAULT 'EAN13',   
    is_primary    INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (item_id) REFERENCES items (id) ON DELETE CASCADE
);

CREATE INDEX idx_item_barcodes_item ON item_barcodes (item_id);

CREATE TABLE item_images (
    id          TEXT PRIMARY KEY,
    item_id     TEXT NOT NULL,
    role        TEXT NOT NULL DEFAULT 'thumbnail', 
    path        TEXT NOT NULL,                     
    sort_order  INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (item_id) REFERENCES items (id) ON DELETE CASCADE
);

CREATE INDEX idx_item_images_item ON item_images (item_id);

CREATE TABLE item_variants (
    id          TEXT PRIMARY KEY,
    item_id     TEXT NOT NULL,   
    sku         TEXT UNIQUE,
    name        TEXT NOT NULL,   
    price       INTEGER NOT NULL, 
    cost_price  INTEGER,
    is_active   INTEGER NOT NULL DEFAULT 1,
    FOREIGN KEY (item_id) REFERENCES items (id) ON DELETE CASCADE
);

CREATE INDEX idx_variants_item ON item_variants (item_id);

CREATE TABLE variant_barcodes (
    barcode       TEXT PRIMARY KEY,
    variant_id    TEXT NOT NULL,
    barcode_type  TEXT DEFAULT 'EAN13',
    is_primary    INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (variant_id) REFERENCES item_variants (id) ON DELETE CASCADE
);

CREATE TABLE inventory (
    id          TEXT PRIMARY KEY,
    item_id     TEXT,
    variant_id  TEXT,
    location_id TEXT NOT NULL,
    quantity    REAL NOT NULL DEFAULT 0,    
    reorder_level REAL DEFAULT 0,
    updated_at  TEXT NOT NULL DEFAULT (datetime('now')),
    FOREIGN KEY (item_id)     REFERENCES items (id),
    FOREIGN KEY (variant_id)  REFERENCES item_variants (id),
    FOREIGN KEY (location_id) REFERENCES stock_locations (id),
    CHECK (
        (item_id IS NOT NULL AND variant_id IS NULL)
        OR (item_id IS NULL AND variant_id IS NOT NULL)
    )
);

CREATE UNIQUE INDEX ux_inventory_item
    ON inventory (item_id, variant_id, location_id);

CREATE TABLE price_history (
    id          TEXT PRIMARY KEY,
    item_id     TEXT,
    variant_id  TEXT,
    price       INTEGER NOT NULL,
    starts_at   TEXT NOT NULL DEFAULT (datetime('now')),
    ends_at     TEXT,
    FOREIGN KEY (item_id)     REFERENCES items (id),
    FOREIGN KEY (variant_id)  REFERENCES item_variants (id),
    CHECK (
        (item_id IS NOT NULL AND variant_id IS NULL)
        OR (item_id IS NULL AND variant_id IS NOT NULL)
    )
);

CREATE INDEX idx_price_history_item
    ON price_history (item_id, variant_id);

CREATE TABLE sales (
    id              TEXT PRIMARY KEY,               
    receipt_no      TEXT NOT NULL UNIQUE,           
    status          TEXT NOT NULL DEFAULT 'completed', 
    sale_type       TEXT NOT NULL DEFAULT 'sale',   
    tender_type     TEXT NOT NULL DEFAULT 'unknown', 
    offline         INTEGER NOT NULL DEFAULT 0,     
    sync_status     TEXT NOT NULL DEFAULT 'queued', 
    sync_attempts   INTEGER NOT NULL DEFAULT 0,
    sync_next_attempt_at TEXT,
    sync_last_error TEXT,
    register_id     TEXT,
    cashier_id      TEXT,
    customer_id     TEXT,
    currency        TEXT NOT NULL DEFAULT 'GBP',
    subtotal        INTEGER NOT NULL,              
    discount_total  INTEGER NOT NULL DEFAULT 0,
    tax_total       INTEGER NOT NULL DEFAULT 0,
    total           INTEGER NOT NULL,              
    rounding        INTEGER NOT NULL DEFAULT 0,    
    note            TEXT,
    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    completed_at    TEXT,
    voided_at       TEXT, till_id TEXT NOT NULL DEFAULT '', service_charge_amount INTEGER NOT NULL DEFAULT 0, order_type TEXT NOT NULL DEFAULT '', order_status TEXT NOT NULL DEFAULT '', order_status_updated_at TEXT, kitchen_print_failed_at TEXT, receipt_print_failed_at TEXT, table_id TEXT REFERENCES tables(id), tracking_token TEXT, service_charge_tax_basis_bp INTEGER NOT NULL DEFAULT 0, voucher_issue_total INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (customer_id) REFERENCES customers (id),
    FOREIGN KEY (register_id) REFERENCES registers (id),
    FOREIGN KEY (cashier_id)  REFERENCES users (id)
);

CREATE INDEX idx_sales_created ON sales (created_at);

CREATE INDEX idx_sales_status  ON sales (status);

CREATE INDEX idx_sales_sync_queue ON sales (sync_status, sync_next_attempt_at);

CREATE TABLE sale_lines (
    id               TEXT PRIMARY KEY,
    sale_id          TEXT NOT NULL,
    line_no          INTEGER NOT NULL,
    item_id          TEXT,
    variant_id       TEXT,
    name_snapshot    TEXT NOT NULL,  
    sku_snapshot     TEXT,
    barcode_snapshot TEXT,
    quantity         REAL NOT NULL,  
    unit_price       INTEGER NOT NULL, 
    line_discount    INTEGER NOT NULL DEFAULT 0,
    tax_rate_bp      INTEGER NOT NULL, 
    tax_amount       INTEGER NOT NULL,
    total_before_tax INTEGER NOT NULL,
    total_after_tax  INTEGER NOT NULL, order_type TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (sale_id)    REFERENCES sales (id) ON DELETE CASCADE,
    FOREIGN KEY (item_id)    REFERENCES items (id),
    FOREIGN KEY (variant_id) REFERENCES item_variants (id),
    CHECK (
        (item_id IS NOT NULL AND variant_id IS NULL)
        OR (item_id IS NULL AND variant_id IS NOT NULL)
    )
);

CREATE UNIQUE INDEX ux_sale_lines_sale_line
    ON sale_lines (sale_id, line_no);

CREATE TABLE payments (
    id          TEXT PRIMARY KEY,
    sale_id     TEXT NOT NULL,
    method_id   TEXT NOT NULL,      
    amount      INTEGER NOT NULL,   
    currency    TEXT NOT NULL DEFAULT 'GBP',
    reference   TEXT,               
    change_given INTEGER NOT NULL DEFAULT 0,
    paid_at     TEXT NOT NULL DEFAULT (datetime('now')), tip_amount INTEGER NOT NULL DEFAULT 0, masked_pan TEXT, auth_code TEXT, terminal_id TEXT, trace_id TEXT, tip_recipient TEXT NOT NULL DEFAULT 'employee', voucher_id TEXT,
    FOREIGN KEY (sale_id)  REFERENCES sales (id) ON DELETE CASCADE,
    FOREIGN KEY (method_id) REFERENCES payment_methods (id)
);

CREATE INDEX idx_payments_sale ON payments (sale_id);

CREATE TABLE sale_discounts (
    id          TEXT PRIMARY KEY,
    sale_id     TEXT NOT NULL,
    line_id     TEXT,          
    type        TEXT NOT NULL, 
    value       INTEGER NOT NULL,  
    amount      INTEGER NOT NULL,  
    reason      TEXT,
    FOREIGN KEY (sale_id) REFERENCES sales (id) ON DELETE CASCADE,
    FOREIGN KEY (line_id) REFERENCES sale_lines (id) ON DELETE CASCADE
);

CREATE INDEX idx_sale_discounts_sale ON sale_discounts (sale_id);

CREATE TABLE sale_links (
    id               TEXT PRIMARY KEY,
    sale_id          TEXT NOT NULL,  
    original_sale_id TEXT NOT NULL,
    reason           TEXT,
    FOREIGN KEY (sale_id)          REFERENCES sales (id) ON DELETE CASCADE,
    FOREIGN KEY (original_sale_id) REFERENCES sales (id)
);

CREATE TABLE shifts (
    id            TEXT PRIMARY KEY,
    register_id   TEXT NOT NULL,
    cashier_id    TEXT NOT NULL,
    opened_at     TEXT NOT NULL DEFAULT (datetime('now')),
    closed_at     TEXT,
    opening_cash  INTEGER NOT NULL DEFAULT 0,
    closing_cash  INTEGER,
    expected_cash INTEGER,
    note          TEXT, new_float INTEGER, count_protocol TEXT,
    FOREIGN KEY (register_id) REFERENCES registers (id),
    FOREIGN KEY (cashier_id)  REFERENCES users (id)
);

CREATE INDEX idx_shifts_register_open
    ON shifts (register_id, opened_at);

CREATE TABLE stock_movements (
    id          TEXT PRIMARY KEY,
    item_id     TEXT,
    variant_id  TEXT,
    location_id TEXT NOT NULL,
    sale_line_id TEXT,           
    type        TEXT NOT NULL,   
    quantity    REAL NOT NULL,   
    cost_price  INTEGER,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    FOREIGN KEY (item_id)      REFERENCES items (id),
    FOREIGN KEY (variant_id)   REFERENCES item_variants (id),
    FOREIGN KEY (location_id)  REFERENCES stock_locations (id),
    FOREIGN KEY (sale_line_id) REFERENCES sale_lines (id),
    CHECK (
        (item_id IS NOT NULL AND variant_id IS NULL)
        OR (item_id IS NULL AND variant_id IS NOT NULL)
    )
);

CREATE INDEX idx_stock_movements_item
    ON stock_movements (item_id, variant_id, created_at);

CREATE TABLE audit_log (
    id          TEXT PRIMARY KEY,
    actor_id    TEXT,            
    entity_type TEXT NOT NULL,   
    entity_id   TEXT NOT NULL,
    action      TEXT NOT NULL,   
    data_json   TEXT,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')), blocked_actor_id TEXT REFERENCES users(id),
    FOREIGN KEY (actor_id) REFERENCES users (id)
);

CREATE INDEX idx_audit_entity
    ON audit_log (entity_type, entity_id);

CREATE TABLE plugin_catalog (
    id                TEXT NOT NULL,  
    version           TEXT NOT NULL,
    name              TEXT NOT NULL,
    description       TEXT,
    author            TEXT,
    website           TEXT,
    repository_url    TEXT,
    runtime           TEXT NOT NULL,  
    entrypoint        TEXT NOT NULL,
    package_url       TEXT NOT NULL,  
    sha256            TEXT NOT NULL,  
    signature         TEXT,           
    size_bytes        INTEGER,
    min_pos_version   TEXT NOT NULL,  
    max_pos_version   TEXT,           
    api_version       TEXT NOT NULL,  
    tags_json         TEXT,           
    capabilities_json TEXT,           
    published_at      TEXT NOT NULL,
    is_deprecated     INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (id, version)
);

CREATE TABLE plugins (
    id                TEXT PRIMARY KEY,       
    name              TEXT NOT NULL,
    version           TEXT NOT NULL,          
    install_state     TEXT NOT NULL DEFAULT 'installed', 
    description       TEXT,
    author            TEXT,
    website           TEXT,
    entrypoint        TEXT NOT NULL,          
    runtime           TEXT NOT NULL DEFAULT 'go', 
    installed_from_url TEXT,
    installed_sha256  TEXT,
    is_active         INTEGER NOT NULL DEFAULT 1,
    trust_level       TEXT NOT NULL DEFAULT 'untrusted', 
    installed_at      TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at        TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (id, version),
    FOREIGN KEY (id, version) REFERENCES plugin_catalog (id, version)
);

CREATE INDEX idx_plugins_active ON plugins (is_active);

CREATE TABLE plugin_entries (
    id              TEXT PRIMARY KEY,                 
    plugin_id       TEXT NOT NULL,

    type            TEXT NOT NULL,                    
    key             TEXT NOT NULL,                    
    label           TEXT NOT NULL,

    icon_path       TEXT,                             
    sort_order      INTEGER NOT NULL DEFAULT 0,
    is_active       INTEGER NOT NULL DEFAULT 1,

    
    parent_page_key TEXT,                             
    menu_group      TEXT,                             
    route           TEXT,                             
    target_action   TEXT,                             
    trigger_event   TEXT,                             

    
    config_json     TEXT,

    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at      TEXT NOT NULL DEFAULT (datetime('now')),

    FOREIGN KEY (plugin_id) REFERENCES plugins(id) ON DELETE CASCADE,
    UNIQUE (plugin_id, key),

    CHECK (type IN (
        'page',
        'button',
        'popup',
        'payment',
        'device',
        'integration',
        'report',
        'pricing',
        'tax',
        'import',
        'export',
        'hardware',
        'background_job',
        'scheduler',
        'receipt_template',
        'customer_facing',
        'auth',
        'notification',
        'delivery',
        'theme'
    ))
);

CREATE INDEX idx_plugin_entries_plugin       ON plugin_entries (plugin_id);

CREATE INDEX idx_plugin_entries_type         ON plugin_entries (type);

CREATE INDEX idx_plugin_entries_parent_page  ON plugin_entries (parent_page_key);

CREATE TABLE plugin_settings (
    id          TEXT PRIMARY KEY,        
    plugin_id   TEXT NOT NULL,
    key         TEXT NOT NULL,           
    value_json  TEXT NOT NULL,           
    scope       TEXT NOT NULL DEFAULT 'global', 
    scope_id    TEXT,                    
    updated_at  TEXT NOT NULL DEFAULT (datetime('now')),
    FOREIGN KEY (plugin_id) REFERENCES plugins (id) ON DELETE CASCADE,
    UNIQUE (plugin_id, key, scope, scope_id)
);

CREATE INDEX idx_plugin_settings_plugin ON plugin_settings (plugin_id);

CREATE TABLE plugin_hooks (
    id          TEXT PRIMARY KEY,
    plugin_id   TEXT NOT NULL,
    event       TEXT NOT NULL,      
    action      TEXT NOT NULL,      
    priority    INTEGER NOT NULL DEFAULT 100,
    is_active   INTEGER NOT NULL DEFAULT 1,
    config_json TEXT,               
    FOREIGN KEY (plugin_id) REFERENCES plugins (id) ON DELETE CASCADE,
    UNIQUE (plugin_id, event, action)
);

CREATE INDEX idx_plugin_hooks_event ON plugin_hooks (event);

CREATE TABLE plugin_permissions (
    id          TEXT PRIMARY KEY,
    plugin_id   TEXT NOT NULL,
    permission  TEXT NOT NULL,      
    granted     INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (plugin_id) REFERENCES plugins (id) ON DELETE CASCADE,
    UNIQUE (plugin_id, permission)
);

CREATE TABLE shortcut_buttons (
    barcode     TEXT PRIMARY KEY,
    item_id     TEXT NOT NULL,
    label       TEXT NOT NULL,
    image_path  TEXT, sort_order INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (item_id) REFERENCES items (id) ON DELETE CASCADE
);

CREATE TABLE held_sales (
    id          TEXT PRIMARY KEY,
    label       TEXT NOT NULL DEFAULT '',
    total_minor INTEGER NOT NULL DEFAULT 0,
    line_count  INTEGER NOT NULL DEFAULT 0,
    payload     TEXT NOT NULL,
    created_at  TEXT NOT NULL DEFAULT (datetime('now'))
, table_id TEXT REFERENCES tables(id));

CREATE TABLE sessions (
    id          TEXT PRIMARY KEY,
    token_hash  TEXT NOT NULL UNIQUE,
    user_id     TEXT NOT NULL,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    expires_at  TEXT NOT NULL,
    revoked_at  TEXT, last_seen_at TEXT,
    FOREIGN KEY (user_id) REFERENCES users (id)
);

CREATE INDEX idx_sessions_user ON sessions (user_id);

CREATE TABLE plugin_install_status (
    listing_id      TEXT PRIMARY KEY,
    plugin_id       TEXT,
    plugin_name     TEXT,
    target_version  TEXT,
    current_version TEXT,
    state           TEXT NOT NULL,
    message_key     TEXT,
    retryable       INTEGER NOT NULL DEFAULT 0,
    updated_at      TEXT NOT NULL
);

CREATE TABLE translation_overrides (
    locale     TEXT NOT NULL,
    key        TEXT NOT NULL,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    updated_by TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (locale, key)
);

CREATE TABLE related_items (
    item_id         TEXT NOT NULL,
    related_item_id TEXT NOT NULL,
    support         INTEGER NOT NULL,   
    score           REAL NOT NULL,      
    updated_at      TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (item_id, related_item_id),
    FOREIGN KEY (item_id)         REFERENCES items (id) ON DELETE CASCADE,
    FOREIGN KEY (related_item_id) REFERENCES items (id) ON DELETE CASCADE
);

CREATE TABLE plugin_storage (
    plugin_id  TEXT NOT NULL,
    key        TEXT NOT NULL,
    value      BLOB NOT NULL,
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (plugin_id, key)
);

CREATE TABLE report_archive (
    id           TEXT PRIMARY KEY,
    kind         TEXT NOT NULL,             
    period       TEXT NOT NULL,             
    content_json TEXT NOT NULL,
    created_at   TEXT NOT NULL DEFAULT (datetime('now')), cloud_acked_at TEXT, z_number INTEGER, prev_z_number INTEGER, prev_closed_at TEXT, first_receipt TEXT, last_receipt TEXT,
    UNIQUE (kind, period)
);

CREATE TABLE invoices (
    id                  TEXT PRIMARY KEY,
    series              TEXT NOT NULL,              
    invoice_no          INTEGER NOT NULL,           
    display_no          TEXT NOT NULL UNIQUE,       
    kind                TEXT NOT NULL DEFAULT 'invoice' CHECK (kind IN ('invoice','credit_note')),
    sale_id             TEXT NOT NULL,
    original_invoice_id TEXT,                       
    customer_name       TEXT NOT NULL,
    customer_address    TEXT NOT NULL DEFAULT '',
    customer_vat_no     TEXT NOT NULL DEFAULT '',
    seller_json         TEXT NOT NULL,              
    net_total           INTEGER NOT NULL,           
    tax_total           INTEGER NOT NULL,
    gross_total         INTEGER NOT NULL,
    vat_breakdown_json  TEXT NOT NULL,              
    issued_at           TEXT NOT NULL,
    issued_by           TEXT NOT NULL,
    FOREIGN KEY (sale_id)             REFERENCES sales (id),
    FOREIGN KEY (original_invoice_id) REFERENCES invoices (id)
);

CREATE UNIQUE INDEX ux_invoices_series_no ON invoices (series, invoice_no);

CREATE UNIQUE INDEX ux_invoices_sale_kind ON invoices (sale_id, kind);

CREATE INDEX idx_invoices_issued ON invoices (issued_at);

CREATE TABLE item_modifier_groups (
    id          TEXT PRIMARY KEY,
    item_id     TEXT NOT NULL,
    name        TEXT NOT NULL,
    required    INTEGER NOT NULL DEFAULT 0,   
    min_select  INTEGER NOT NULL DEFAULT 0,
    max_select  INTEGER NOT NULL DEFAULT 1,   
    sort_order  INTEGER NOT NULL DEFAULT 0,
    is_active   INTEGER NOT NULL DEFAULT 1,
    FOREIGN KEY (item_id) REFERENCES items (id) ON DELETE CASCADE,
    CHECK (min_select >= 0 AND max_select >= min_select)
);

CREATE INDEX idx_item_modifier_groups_item
    ON item_modifier_groups (item_id);

CREATE TABLE item_modifier_options (
    id                TEXT PRIMARY KEY,
    group_id          TEXT NOT NULL,
    name              TEXT NOT NULL,
    price_delta_minor INTEGER NOT NULL DEFAULT 0,  
    sort_order        INTEGER NOT NULL DEFAULT 0,
    is_active         INTEGER NOT NULL DEFAULT 1,
    FOREIGN KEY (group_id) REFERENCES item_modifier_groups (id) ON DELETE CASCADE,
    CHECK (price_delta_minor >= 0)
);

CREATE INDEX idx_item_modifier_options_group
    ON item_modifier_options (group_id);

CREATE TABLE sale_line_modifiers (
    id                     TEXT PRIMARY KEY,
    sale_line_id           TEXT NOT NULL,
    group_id               TEXT,             
    option_id              TEXT,             
    group_name_snapshot    TEXT NOT NULL,
    option_name_snapshot   TEXT NOT NULL,
    price_delta_minor      INTEGER NOT NULL,
    FOREIGN KEY (sale_line_id) REFERENCES sale_lines (id) ON DELETE CASCADE
);

CREATE INDEX idx_sale_line_modifiers_line
    ON sale_line_modifiers (sale_line_id);

CREATE TABLE pending_pairings (
    id           TEXT PRIMARY KEY,
    device_name  TEXT NOT NULL,
    commitment   TEXT NOT NULL,
    token        TEXT NOT NULL DEFAULT '',
    requested_at TEXT NOT NULL,
    expires_at   TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'pending'
);

CREATE INDEX idx_pending_pairings_status ON pending_pairings(status);

CREATE INDEX idx_pending_pairings_expires_at ON pending_pairings(expires_at);

CREATE TABLE "tills" (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    bearer_hash  TEXT UNIQUE,
    enrolled_at  TEXT NOT NULL DEFAULT (datetime('now')),
    last_seen_at TEXT
);

CREATE TABLE issue_reports_sent (
    id               TEXT PRIMARY KEY,
    note             TEXT NOT NULL DEFAULT '',
    captured_at      TEXT NOT NULL,
    had_audio        INTEGER NOT NULL DEFAULT 0,
    had_video        INTEGER NOT NULL DEFAULT 0,
    image_count      INTEGER NOT NULL DEFAULT 0,
    status           TEXT NOT NULL DEFAULT 'sent',
    github_issue_url TEXT NOT NULL DEFAULT '',
    last_synced_at   TEXT
);

CREATE INDEX idx_issue_reports_sent_captured_at ON issue_reports_sent(captured_at);

CREATE TABLE order_status_events (
    id         TEXT PRIMARY KEY,
    receipt_no TEXT NOT NULL,
    status     TEXT NOT NULL,
    actor_id   TEXT,
    created_at TEXT NOT NULL
);

CREATE INDEX idx_order_status_events_receipt ON order_status_events(receipt_no, created_at);

CREATE TABLE kitchen_stations (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    destination_type TEXT NOT NULL DEFAULT 'printer' CHECK (destination_type IN ('printer','display')),
    printer_address  TEXT,
    enabled          INTEGER NOT NULL DEFAULT 1,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);

CREATE TABLE item_station_routes (
    item_id    TEXT NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    station_id TEXT NOT NULL REFERENCES kitchen_stations(id) ON DELETE CASCADE,
    PRIMARY KEY (item_id, station_id)
);

CREATE INDEX idx_item_station_routes_station ON item_station_routes(station_id);

CREATE TABLE category_station_routes (
    category_id TEXT NOT NULL REFERENCES categories(id) ON DELETE CASCADE,
    station_id  TEXT NOT NULL REFERENCES kitchen_stations(id) ON DELETE CASCADE,
    PRIMARY KEY (category_id, station_id)
);

CREATE INDEX idx_category_station_routes_station ON category_station_routes(station_id);

CREATE TABLE roles (
    role TEXT PRIMARY KEY 
);

CREATE TABLE permission_actions (
    action TEXT PRIMARY KEY
);

CREATE TABLE role_permissions (
    role    TEXT NOT NULL REFERENCES roles(role),
    action  TEXT NOT NULL REFERENCES permission_actions(action),
    granted INTEGER NOT NULL DEFAULT 0, 
    PRIMARY KEY (role, action)
);

CREATE TABLE reset_batches (
    id          TEXT PRIMARY KEY,
    created_at  TEXT NOT NULL,
    actor_id    TEXT,
    sales_count INTEGER NOT NULL DEFAULT 0
);

-- ----------------------------------------
-- Reset archives (ADR-0042, ut-docs#187) — restated here after ADR-0074's
-- squash deleted this invariant's original home (migration 040's header).
-- ----------------------------------------
-- POSRepo.ResetTransactionHistory does not destroy transactional data:
-- every row it clears is moved into a *_archive twin, tagged with one
-- reset_batches row per reset event, and is restorable whole-batch as long
-- as the till has not traded since (see reset_archive_repo.go).
-- report_archive above is NOT part of this mechanism (ADR-0040 §9) — it is
-- a separate retained legal record with its own retention pruning.
--
-- Every table below must stay column-identical to its live counterpart,
-- plus reset_batch_id, with three deliberate relaxations: no FKs to live
-- tables (an archived row must not pin, or be broken by, live rows that
-- can change or vanish after the reset — sale_id/sale_line_id links only
-- make sense WITHIN the same batch); no PRIMARY KEY/UNIQUE constraints (a
-- shop that resets, trades, and resets again legitimately archives the
-- same receipt_no/display_no in two different batches); NOT NULL and
-- single-table CHECK constraints are kept. Whoever adds a column to a live
-- table that has an archive twin must add the same column here too, in
-- the same change (internal/data/reset_test.go pins this).
CREATE TABLE sales_archive (
    id              TEXT NOT NULL,
    receipt_no      TEXT NOT NULL,
    status          TEXT NOT NULL,
    sale_type       TEXT NOT NULL,
    tender_type     TEXT NOT NULL,
    offline         INTEGER NOT NULL,
    sync_status     TEXT NOT NULL,
    sync_attempts   INTEGER NOT NULL,
    sync_next_attempt_at TEXT,
    sync_last_error TEXT,
    register_id     TEXT,
    cashier_id      TEXT,
    customer_id     TEXT,
    currency        TEXT NOT NULL,
    subtotal        INTEGER NOT NULL,
    discount_total  INTEGER NOT NULL,
    tax_total       INTEGER NOT NULL,
    total           INTEGER NOT NULL,
    rounding        INTEGER NOT NULL,
    note            TEXT,
    created_at      TEXT NOT NULL,
    completed_at    TEXT,
    voided_at       TEXT,
    till_id         TEXT NOT NULL,
    service_charge_amount INTEGER NOT NULL,
    order_type      TEXT NOT NULL,
    order_status    TEXT NOT NULL,
    order_status_updated_at TEXT,
    kitchen_print_failed_at TEXT,
    receipt_print_failed_at TEXT,
    reset_batch_id  TEXT NOT NULL REFERENCES reset_batches (id)
, table_id TEXT REFERENCES tables(id), tracking_token TEXT, service_charge_tax_basis_bp INTEGER NOT NULL DEFAULT 0, voucher_issue_total INTEGER NOT NULL DEFAULT 0);

CREATE INDEX idx_sales_archive_batch ON sales_archive (reset_batch_id);

CREATE TABLE sale_lines_archive (
    id               TEXT NOT NULL,
    sale_id          TEXT NOT NULL,
    line_no          INTEGER NOT NULL,
    item_id          TEXT,
    variant_id       TEXT,
    name_snapshot    TEXT NOT NULL,
    sku_snapshot     TEXT,
    barcode_snapshot TEXT,
    quantity         REAL NOT NULL,
    unit_price       INTEGER NOT NULL,
    line_discount    INTEGER NOT NULL,
    tax_rate_bp      INTEGER NOT NULL,
    tax_amount       INTEGER NOT NULL,
    total_before_tax INTEGER NOT NULL,
    total_after_tax  INTEGER NOT NULL,
    reset_batch_id   TEXT NOT NULL REFERENCES reset_batches (id), order_type TEXT NOT NULL DEFAULT '',
    CHECK (
        (item_id IS NOT NULL AND variant_id IS NULL)
        OR (item_id IS NULL AND variant_id IS NOT NULL)
    )
);

CREATE INDEX idx_sale_lines_archive_batch ON sale_lines_archive (reset_batch_id);

CREATE TABLE sale_line_modifiers_archive (
    id                   TEXT NOT NULL,
    sale_line_id         TEXT NOT NULL,
    group_id             TEXT,
    option_id            TEXT,
    group_name_snapshot  TEXT NOT NULL,
    option_name_snapshot TEXT NOT NULL,
    price_delta_minor    INTEGER NOT NULL,
    reset_batch_id       TEXT NOT NULL REFERENCES reset_batches (id)
);

CREATE INDEX idx_sale_line_modifiers_archive_batch ON sale_line_modifiers_archive (reset_batch_id);

CREATE TABLE sale_discounts_archive (
    id             TEXT NOT NULL,
    sale_id        TEXT NOT NULL,
    line_id        TEXT,
    type           TEXT NOT NULL,
    value          INTEGER NOT NULL,
    amount         INTEGER NOT NULL,
    reason         TEXT,
    reset_batch_id TEXT NOT NULL REFERENCES reset_batches (id)
);

CREATE INDEX idx_sale_discounts_archive_batch ON sale_discounts_archive (reset_batch_id);

CREATE TABLE sale_links_archive (
    id               TEXT NOT NULL,
    sale_id          TEXT NOT NULL,
    original_sale_id TEXT NOT NULL,
    reason           TEXT,
    reset_batch_id   TEXT NOT NULL REFERENCES reset_batches (id)
);

CREATE INDEX idx_sale_links_archive_batch ON sale_links_archive (reset_batch_id);

CREATE TABLE payments_archive (
    id             TEXT NOT NULL,
    sale_id        TEXT NOT NULL,
    method_id      TEXT NOT NULL,
    amount         INTEGER NOT NULL,
    currency       TEXT NOT NULL,
    reference      TEXT,
    change_given   INTEGER NOT NULL,
    paid_at        TEXT NOT NULL,
    tip_amount     INTEGER NOT NULL,
    reset_batch_id TEXT NOT NULL REFERENCES reset_batches (id)
, masked_pan TEXT, auth_code TEXT, terminal_id TEXT, trace_id TEXT, tip_recipient TEXT NOT NULL DEFAULT 'employee', voucher_id TEXT);

CREATE INDEX idx_payments_archive_batch ON payments_archive (reset_batch_id);

CREATE TABLE invoices_archive (
    id                  TEXT NOT NULL,
    series              TEXT NOT NULL,
    invoice_no          INTEGER NOT NULL,
    display_no          TEXT NOT NULL,
    kind                TEXT NOT NULL CHECK (kind IN ('invoice','credit_note')),
    sale_id             TEXT NOT NULL,
    original_invoice_id TEXT,
    customer_name       TEXT NOT NULL,
    customer_address    TEXT NOT NULL,
    customer_vat_no     TEXT NOT NULL,
    seller_json         TEXT NOT NULL,
    net_total           INTEGER NOT NULL,
    tax_total           INTEGER NOT NULL,
    gross_total         INTEGER NOT NULL,
    vat_breakdown_json  TEXT NOT NULL,
    issued_at           TEXT NOT NULL,
    issued_by           TEXT NOT NULL,
    reset_batch_id      TEXT NOT NULL REFERENCES reset_batches (id)
);

CREATE INDEX idx_invoices_archive_batch ON invoices_archive (reset_batch_id);

CREATE TABLE held_sales_archive (
    id             TEXT NOT NULL,
    label          TEXT NOT NULL,
    total_minor    INTEGER NOT NULL,
    line_count     INTEGER NOT NULL,
    payload        TEXT NOT NULL,
    created_at     TEXT NOT NULL,
    reset_batch_id TEXT NOT NULL REFERENCES reset_batches (id)
, table_id TEXT REFERENCES tables(id));

CREATE INDEX idx_held_sales_archive_batch ON held_sales_archive (reset_batch_id);

CREATE TABLE shifts_archive (
    id             TEXT NOT NULL,
    register_id    TEXT NOT NULL,
    cashier_id     TEXT NOT NULL,
    opened_at      TEXT NOT NULL,
    closed_at      TEXT,
    opening_cash   INTEGER NOT NULL,
    closing_cash   INTEGER,
    expected_cash  INTEGER,
    note           TEXT,
    reset_batch_id TEXT NOT NULL REFERENCES reset_batches (id)
, new_float INTEGER, count_protocol TEXT);

CREATE INDEX idx_shifts_archive_batch ON shifts_archive (reset_batch_id);

CREATE TABLE stock_movements_archive (
    id             TEXT NOT NULL,
    item_id        TEXT,
    variant_id     TEXT,
    location_id    TEXT NOT NULL,
    sale_line_id   TEXT,
    type           TEXT NOT NULL,
    quantity       REAL NOT NULL,
    cost_price     INTEGER,
    created_at     TEXT NOT NULL,
    reset_batch_id TEXT NOT NULL REFERENCES reset_batches (id),
    CHECK (
        (item_id IS NOT NULL AND variant_id IS NULL)
        OR (item_id IS NULL AND variant_id IS NOT NULL)
    )
);

CREATE INDEX idx_stock_movements_archive_batch ON stock_movements_archive (reset_batch_id);

CREATE TABLE country_settings (
    code             TEXT PRIMARY KEY,
    name_key         TEXT NOT NULL,
    currency         TEXT NOT NULL DEFAULT '',
    currency_symbol  TEXT NOT NULL DEFAULT '',
    tax_rate_bp      INTEGER NOT NULL DEFAULT 0 CHECK (tax_rate_bp >= 0),
    tax_inclusive    INTEGER NOT NULL DEFAULT 1 CHECK (tax_inclusive IN (0,1)),
    archive_min_days INTEGER NOT NULL CHECK (archive_min_days >= 0),
    is_builtin       INTEGER NOT NULL DEFAULT 0 CHECK (is_builtin IN (0,1)),
    updated_at       TEXT NOT NULL
, default_locale TEXT NOT NULL DEFAULT '');

CREATE TABLE fiscal_tse_signatures (
    sale_id             TEXT PRIMARY KEY,
    transaction_number  INTEGER NOT NULL DEFAULT 0,
    signature_counter   INTEGER NOT NULL DEFAULT 0,
    serial_number       TEXT NOT NULL DEFAULT '',
    start_time          TEXT NOT NULL DEFAULT '',
    log_time            TEXT NOT NULL DEFAULT '',
    signature           TEXT NOT NULL,
    signature_algorithm TEXT NOT NULL DEFAULT '',
    created_at          TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE UNIQUE INDEX ux_plugin_settings_global
    ON plugin_settings (plugin_id, key)
    WHERE scope = 'global';

CREATE TABLE tables (
    id         TEXT PRIMARY KEY,
    label      TEXT NOT NULL,                
    area_zone  TEXT NOT NULL DEFAULT '',     
    seat_count INTEGER NOT NULL DEFAULT 0,
    shape      TEXT NOT NULL DEFAULT 'rect' CHECK (shape IN ('rect','round')),
    pos_x      INTEGER NOT NULL DEFAULT 0,   
    pos_y      INTEGER NOT NULL DEFAULT 0,
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX idx_held_sales_table ON held_sales(table_id);

CREATE INDEX idx_sales_table ON sales(table_id);

CREATE UNIQUE INDEX idx_sales_tracking_token ON sales(tracking_token) WHERE tracking_token IS NOT NULL;

CREATE TABLE sale_charges (
    sale_id      TEXT    NOT NULL REFERENCES sales (id),
    seq          INTEGER NOT NULL,   
    key          TEXT    NOT NULL,   
    label        TEXT    NOT NULL DEFAULT '', 
    amount_minor INTEGER NOT NULL,   
                                      
                                      
    tax_basis_bp INTEGER NOT NULL DEFAULT 0, 
                                      
    base         TEXT    NOT NULL DEFAULT 'net_lines',
    PRIMARY KEY (sale_id, seq)
);

CREATE TABLE sale_charges_archive (
    sale_id        TEXT    NOT NULL,
    seq            INTEGER NOT NULL,
    key            TEXT    NOT NULL,
    label          TEXT    NOT NULL DEFAULT '',
    amount_minor   INTEGER NOT NULL,
    tax_basis_bp   INTEGER NOT NULL DEFAULT 0,
    base           TEXT    NOT NULL DEFAULT 'net_lines',
    reset_batch_id TEXT    NOT NULL REFERENCES reset_batches (id)
);

CREATE INDEX idx_sale_charges_archive_batch ON sale_charges_archive (reset_batch_id);

CREATE TABLE worker_allocations (
    id            TEXT    NOT NULL PRIMARY KEY,
    source_type   TEXT    NOT NULL CHECK (source_type IN ('tip', 'service_charge', 'yuzde_usulu_pool')),
    source_id     TEXT    NOT NULL DEFAULT '',   
    cashier_id    TEXT    NOT NULL,              
    amount_minor  INTEGER NOT NULL,              
    allocated_at  TEXT    NOT NULL,              
    note          TEXT    NOT NULL DEFAULT ''    
);

CREATE INDEX idx_worker_allocations_cashier ON worker_allocations (cashier_id);

CREATE INDEX idx_worker_allocations_source ON worker_allocations (source_type, source_id);

CREATE INDEX idx_worker_allocations_allocated_at ON worker_allocations (allocated_at);

CREATE TABLE worker_allocations_archive (
    id            TEXT    NOT NULL,
    source_type   TEXT    NOT NULL CHECK (source_type IN ('tip', 'service_charge', 'yuzde_usulu_pool')),
    source_id     TEXT    NOT NULL DEFAULT '',
    cashier_id    TEXT    NOT NULL,
    amount_minor  INTEGER NOT NULL,
    allocated_at  TEXT    NOT NULL,
    note          TEXT    NOT NULL DEFAULT '',
    reset_batch_id TEXT   NOT NULL REFERENCES reset_batches (id)
);

CREATE INDEX idx_worker_allocations_archive_batch ON worker_allocations_archive (reset_batch_id);

CREATE TABLE vouchers (
    id                TEXT PRIMARY KEY,             
    holder_label      TEXT,                         
    original_amount   INTEGER NOT NULL,             
    balance           INTEGER NOT NULL,             
    currency          TEXT NOT NULL DEFAULT 'EUR',
    voucher_type      TEXT NOT NULL DEFAULT 'multi_purpose'
                          CHECK (voucher_type IN ('multi_purpose')),
    status            TEXT NOT NULL DEFAULT 'active'
                          CHECK (status IN ('active','redeemed','void')),
    issued_sale_id    TEXT,                         
    created_at        TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE voucher_transactions (
    id           TEXT PRIMARY KEY,
    voucher_id   TEXT NOT NULL REFERENCES vouchers (id),
    sale_id      TEXT,                              
    type         TEXT NOT NULL CHECK (type IN ('issue','redemption')),
    amount       INTEGER NOT NULL,                  
    created_at   TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_voucher_tx_voucher ON voucher_transactions (voucher_id);

CREATE UNIQUE INDEX ux_report_archive_kind_znumber
    ON report_archive (kind, z_number) WHERE z_number IS NOT NULL;

CREATE TABLE sync_journal_quarantine (
    id             TEXT PRIMARY KEY,
    till_id        TEXT NOT NULL,
    sale_id        TEXT NOT NULL,
    receipt_no     TEXT NOT NULL,
    reason         TEXT NOT NULL,
    payload_json   TEXT NOT NULL,
    quarantined_at TEXT NOT NULL,
    UNIQUE (sale_id)
);

CREATE INDEX idx_sync_journal_quarantine_till ON sync_journal_quarantine (till_id);

CREATE INDEX idx_sales_status_created_dt
    ON sales (status, datetime(created_at));

CREATE INDEX idx_audit_log_entity_action_created_dt
    ON audit_log (entity_type, action, datetime(created_at));

CREATE INDEX idx_variant_barcodes_variant
    ON variant_barcodes (variant_id);

CREATE INDEX idx_sale_links_sale
    ON sale_links (sale_id);

CREATE INDEX idx_sale_links_original_sale
    ON sale_links (original_sale_id);

CREATE TABLE table_claims (
    table_id   TEXT PRIMARY KEY REFERENCES tables(id),
    claimed_at TEXT NOT NULL
);

-- ----------------------------------------
-- Seed rows
-- ----------------------------------------
-- settings (2 rows)
INSERT INTO "settings" ("key", "value") VALUES
    ('store.name', 'My Store'),
    ('store.currency', 'GBP');

-- tax_codes (3 rows)
INSERT INTO "tax_codes" ("id", "name", "rate_basis_points", "is_active", "takeaway_rate_basis_points") VALUES
    ('tax_std', 'Standard VAT', 2000, 1, NULL),
    ('tax_red', 'Reduced VAT', 500, 1, NULL),
    ('tax_zero', 'Zero-rated', 0, 1, NULL);

-- stock_locations (3 rows)
INSERT INTO "stock_locations" ("id", "name", "is_active", "address_street", "address_postcode", "address_city") VALUES
    ('loc_main', 'Main Store', 1, NULL, NULL, NULL),
    ('loc_back', 'Back Store', 1, NULL, NULL, NULL),
    ('loc_wh', 'Warehouse', 1, NULL, NULL, NULL);

-- users (2 rows)
INSERT INTO "users" ("id", "username", "display_name", "role", "pin_hash", "is_active") VALUES
    ('system', 'system', 'System', 'admin', NULL, 1),
    ('kiosk', 'kiosk', 'Self-order kiosk', 'cashier', NULL, 1);

-- payment_methods (3 rows)
INSERT INTO "payment_methods" ("id", "name", "type", "is_active", "sort_order", "plugin_id") VALUES
    ('cash', 'Cash', 'cash', 1, 1, NULL),
    ('card', 'Card', 'card', 1, 2, NULL),
    ('gift', 'Gift Card', 'voucher', 1, 3, NULL);

-- roles (4 rows)
INSERT INTO "roles" ("role") VALUES
    ('cashier'),
    ('manager'),
    ('admin'),
    ('super_admin');

-- permission_actions (19 rows)
INSERT INTO "permission_actions" ("action") VALUES
    ('refund'),
    ('eod_report'),
    ('cash_adjustment'),
    ('price_override'),
    ('void'),
    ('user_management'),
    ('settings'),
    ('reports'),
    ('audit'),
    ('plugin_management'),
    ('data_management'),
    ('sync_management'),
    ('import_export'),
    ('issue_reporting'),
    ('fiscal_tse_override'),
    ('permission_management'),
    ('tax_code_management'),
    ('stock_location_management'),
    ('worker_allocation');

-- role_permissions (54 rows)
INSERT INTO "role_permissions" ("role", "action", "granted") VALUES
    ('admin', 'refund', 1),
    ('admin', 'eod_report', 1),
    ('admin', 'cash_adjustment', 1),
    ('admin', 'price_override', 1),
    ('admin', 'void', 1),
    ('admin', 'user_management', 1),
    ('admin', 'settings', 1),
    ('manager', 'refund', 1),
    ('manager', 'eod_report', 1),
    ('manager', 'cash_adjustment', 1),
    ('manager', 'price_override', 1),
    ('manager', 'void', 1),
    ('manager', 'user_management', 1),
    ('manager', 'settings', 1),
    ('super_admin', 'refund', 1),
    ('super_admin', 'eod_report', 1),
    ('super_admin', 'cash_adjustment', 1),
    ('super_admin', 'price_override', 1),
    ('super_admin', 'void', 1),
    ('super_admin', 'user_management', 1),
    ('super_admin', 'settings', 1),
    ('admin', 'audit', 1),
    ('admin', 'reports', 1),
    ('manager', 'audit', 1),
    ('manager', 'reports', 1),
    ('super_admin', 'audit', 1),
    ('super_admin', 'reports', 1),
    ('admin', 'plugin_management', 1),
    ('manager', 'plugin_management', 1),
    ('super_admin', 'plugin_management', 1),
    ('admin', 'data_management', 1),
    ('admin', 'sync_management', 1),
    ('manager', 'data_management', 1),
    ('manager', 'sync_management', 1),
    ('super_admin', 'data_management', 1),
    ('super_admin', 'sync_management', 1),
    ('admin', 'import_export', 1),
    ('admin', 'issue_reporting', 1),
    ('manager', 'import_export', 1),
    ('manager', 'issue_reporting', 1),
    ('super_admin', 'import_export', 1),
    ('super_admin', 'issue_reporting', 1),
    ('admin', 'fiscal_tse_override', 1),
    ('super_admin', 'fiscal_tse_override', 1),
    ('super_admin', 'permission_management', 1),
    ('admin', 'tax_code_management', 1),
    ('manager', 'tax_code_management', 1),
    ('super_admin', 'tax_code_management', 1),
    ('admin', 'stock_location_management', 1),
    ('manager', 'stock_location_management', 1),
    ('super_admin', 'stock_location_management', 1),
    ('admin', 'worker_allocation', 1),
    ('manager', 'worker_allocation', 1),
    ('super_admin', 'worker_allocation', 1);

-- country_settings (14 rows)
INSERT INTO "country_settings" ("code", "name_key", "currency", "currency_symbol", "tax_rate_bp", "tax_inclusive", "archive_min_days", "is_builtin", "updated_at", "default_locale") VALUES
    ('GB', 'setup.country.gb', 'GBP', '£', 2000, 1, 3650, 1, '1970-01-01T00:00:00Z', 'en-GB'),
    ('IR', 'setup.country.ir', 'IRT', '', 1000, 1, 3650, 1, '1970-01-01T00:00:00Z', 'fa-IR'),
    ('US', 'setup.country.us', 'USD', '$', 0, 0, 3650, 1, '1970-01-01T00:00:00Z', 'en-US'),
    ('DE', 'setup.country.de', 'EUR', '€', 1900, 1, 3650, 1, '1970-01-01T00:00:00Z', 'de-DE'),
    ('FR', 'setup.country.fr', 'EUR', '€', 2000, 1, 3650, 1, '1970-01-01T00:00:00Z', 'fr-FR'),
    ('ES', 'setup.country.es', 'EUR', '€', 2100, 1, 3650, 1, '1970-01-01T00:00:00Z', 'es-ES'),
    ('IT', 'setup.country.it', 'EUR', '€', 2200, 1, 3650, 1, '1970-01-01T00:00:00Z', 'it-IT'),
    ('NL', 'setup.country.nl', 'EUR', '€', 2100, 1, 3650, 1, '1970-01-01T00:00:00Z', 'nl-NL'),
    ('TR', 'setup.country.tr', 'TRY', '₺', 2000, 1, 3650, 1, '1970-01-01T00:00:00Z', 'tr-TR'),
    ('AE', 'setup.country.ae', 'AED', '', 500, 1, 3650, 1, '1970-01-01T00:00:00Z', 'ar-AE'),
    ('SA', 'setup.country.sa', 'SAR', '', 1500, 1, 3650, 1, '1970-01-01T00:00:00Z', 'ar-SA'),
    ('IN', 'setup.country.in', 'INR', '₹', 1800, 1, 3650, 1, '1970-01-01T00:00:00Z', 'en-IN'),
    ('PK', 'setup.country.pk', 'PKR', '', 1800, 1, 3650, 1, '1970-01-01T00:00:00Z', 'ur-PK'),
    ('OTHER', 'setup.country.other', '', '', 0, 1, 3650, 1, '1970-01-01T00:00:00Z', '');

-- ----------------------------------------
-- Schema lineage (ADR-0074 Decision 3)
-- ----------------------------------------
-- A database that carries schema_migrations rows but no schema_lineage row
-- predates the 2026-09 baseline reset; db.Open refuses to start on it.
CREATE TABLE schema_lineage (
    id           INTEGER PRIMARY KEY CHECK (id = 1),
    reset_marker TEXT NOT NULL,
    reset_at     TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO schema_lineage (id, reset_marker) VALUES (1, '2026-09-migration-baseline-reset');
