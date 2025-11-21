-- Create schema_migrations table to track applied migrations
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS buttons (
    code TEXT PRIMARY KEY,
    label TEXT NOT NULL,
    image_path TEXT
);

-- plugins

CREATE TABLE IF NOT EXISTS plugin_catalog (
    id TEXT NOT NULL, -- plugin id
    version TEXT NOT NULL,
    name TEXT NOT NULL,
    description TEXT,
    author TEXT,
    website TEXT,
    repository_url TEXT,
    runtime TEXT NOT NULL, -- go|wasm|node|python|native
    entrypoint TEXT NOT NULL,
    package_url TEXT NOT NULL, -- where to download
    sha256 TEXT NOT NULL, -- integrity check
    signature TEXT, -- optional signed manifest
    size_bytes INTEGER,
    min_pos_version TEXT NOT NULL, -- compatibility floor
    max_pos_version TEXT, -- optional ceiling
    api_version TEXT NOT NULL, -- plugin API contract version
    tags_json TEXT, -- ["loyalty","payments"]
    capabilities_json TEXT, -- ["payments:charge","devices:usb"]
    published_at TEXT NOT NULL,
    is_deprecated INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (id, version)
);

CREATE TABLE IF NOT EXISTS plugins (
    id TEXT PRIMARY KEY, -- e.g. "com.universaltill.loyalty"
    name TEXT NOT NULL,
    version TEXT NOT NULL, -- semver "1.2.0"
    install_state TEXT NOT NULL DEFAULT 'installed', -- installed|disabled|broken|installing|uninstalling
    description TEXT,
    author TEXT,
    website TEXT,
    entrypoint TEXT NOT NULL, -- executable / wasm / go module / js bundle
    runtime TEXT NOT NULL DEFAULT 'go', -- go|wasm|node|python|native
    installed_from_url TEXT,
    installed_sha256 TEXT,
    is_active INTEGER NOT NULL DEFAULT 1,
    trust_level TEXT NOT NULL DEFAULT 'untrusted', -- untrusted|trusted|system
    installed_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (id, version) FOREIGN KEY (id, version) REFERENCES plugin_catalog (id, version)
);

CREATE INDEX IF NOT EXISTS idx_plugins_active ON plugins (is_active);


CREATE TABLE IF NOT EXISTS plugin_entries (
    id              TEXT PRIMARY KEY,                 -- uuid
    plugin_id       TEXT NOT NULL,

    type            TEXT NOT NULL,                    -- page|button|popup|payment|device|integration|report|pricing|tax|import|export|hardware|background_job|scheduler|receipt_template|customer_facing|auth|notification|delivery
    key             TEXT NOT NULL,                    -- unique inside plugin (e.g. "loyalty.page")
    label           TEXT NOT NULL,

    icon_path       TEXT,                             -- optional icon for menus/buttons
    sort_order      INTEGER NOT NULL DEFAULT 0,
    is_active       INTEGER NOT NULL DEFAULT 1,

-- Navigation / placement (nullable depending on type)
parent_page_key TEXT, -- for buttons/popups: which page they belong to
menu_group TEXT, -- for pages: top menu group like "Sales", "Admin"
route TEXT, -- for pages: internal route "/loyalty"
target_action TEXT, -- for buttons: action name to invoke
trigger_event TEXT, -- for popups: event name (e.g. "sale.completed")

-- Arbitrary plugin-defined config, stored as JSON text

config_json     TEXT,

    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at      TEXT NOT NULL DEFAULT (datetime('now')),

    FOREIGN KEY(plugin_id) REFERENCES plugins(id) ON DELETE CASCADE,
    UNIQUE(plugin_id, key),

    CHECK (type IN ('page','button','popup','payment','device'))
);

CREATE INDEX IF NOT EXISTS idx_plugin_entries_plugin ON plugin_entries (plugin_id);

CREATE INDEX IF NOT EXISTS idx_plugin_entries_type ON plugin_entries(type);

CREATE INDEX IF NOT EXISTS idx_plugin_entries_parent_page ON plugin_entries (parent_page_key);

CREATE TABLE IF NOT EXISTS plugin_settings (
    id TEXT PRIMARY KEY, -- uuid
    plugin_id TEXT NOT NULL,
    key TEXT NOT NULL, -- e.g. "apiKey", "terminalIp"
    value_json TEXT NOT NULL, -- store typed configs
    scope TEXT NOT NULL DEFAULT 'global', -- global|register|user
    scope_id TEXT, -- register_id/user_id if scoped
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    FOREIGN KEY (plugin_id) REFERENCES plugins (id) ON DELETE CASCADE,
    UNIQUE (
        plugin_id,
        key,
        scope,
        scope_id
    )
);

CREATE INDEX IF NOT EXISTS idx_plugin_settings_plugin ON plugin_settings (plugin_id);

CREATE TABLE IF NOT EXISTS plugin_hooks (
    id TEXT PRIMARY KEY,
    plugin_id TEXT NOT NULL,
    event TEXT NOT NULL, -- e.g. "sale.completed"
    action TEXT NOT NULL, -- e.g. "loyalty.awardPoints"
    priority INTEGER NOT NULL DEFAULT 100,
    is_active INTEGER NOT NULL DEFAULT 1,
    config_json TEXT, -- optional per-hook config
    FOREIGN KEY (plugin_id) REFERENCES plugins (id) ON DELETE CASCADE,
    UNIQUE (plugin_id, event, action)
);

CREATE INDEX IF NOT EXISTS idx_plugin_hooks_event ON plugin_hooks(event);

CREATE TABLE IF NOT EXISTS plugin_permissions (
    id TEXT PRIMARY KEY,
    plugin_id TEXT NOT NULL,
    permission TEXT NOT NULL, -- e.g. "payments:charge", "devices:usb", "sales:read"
    granted INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (plugin_id) REFERENCES plugins (id) ON DELETE CASCADE,
    UNIQUE (plugin_id, permission)
);

-- Global key/value settings for the POS instance
CREATE TABLE IF NOT EXISTS settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS items (
    id TEXT PRIMARY KEY, -- uuid or your code
    sku TEXT UNIQUE, -- internal SKU / PLU
    name TEXT NOT NULL,
    description TEXT,
    category_id TEXT,
    brand_id TEXT,
    unit TEXT NOT NULL DEFAULT 'each', -- each, kg, liter, etc
    base_price INTEGER NOT NULL, -- cents/pence to avoid float
    cost_price INTEGER, -- optional, cents
    tax_code_id TEXT, -- maps to tax table
    is_active INTEGER NOT NULL DEFAULT 1,
    is_weighed INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    FOREIGN KEY (category_id) REFERENCES categories (id),
    FOREIGN KEY (brand_id) REFERENCES brands (id),
    FOREIGN KEY (tax_code_id) REFERENCES tax_codes (id)
);

CREATE INDEX IF NOT EXISTS idx_items_category ON items (category_id);

CREATE INDEX IF NOT EXISTS idx_items_active ON items (is_active);

CREATE TABLE IF NOT EXISTS item_barcodes (
    barcode TEXT PRIMARY KEY,
    item_id TEXT NOT NULL,
    barcode_type TEXT DEFAULT 'EAN13', -- EAN13, UPC, QR, etc
    is_primary INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (item_id) REFERENCES items (id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_item_barcodes_item ON item_barcodes (item_id);

CREATE TABLE IF NOT EXISTS categories (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    parent_id TEXT,
    sort_order INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (parent_id) REFERENCES categories (id)
);

CREATE INDEX IF NOT EXISTS idx_categories_parent ON categories (parent_id);

CREATE TABLE IF NOT EXISTS brands (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE
);

CREATE TABLE IF NOT EXISTS item_images (
    id TEXT PRIMARY KEY,
    item_id TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'thumbnail', -- thumbnail, gallery, label
    path TEXT NOT NULL, -- e.g. assets/items/coke.png
    sort_order INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (item_id) REFERENCES items (id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_item_images_item ON item_images (item_id);

CREATE TABLE IF NOT EXISTS tax_codes (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE, -- "Standard VAT", "Zero-rated"
    rate_basis_points INTEGER NOT NULL, -- e.g. 2000 = 20.00%
    is_active INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS item_variants (
    id TEXT PRIMARY KEY,
    item_id TEXT NOT NULL, -- parent item
    sku TEXT UNIQUE,
    name TEXT NOT NULL, -- "330ml", "500ml Bottle"
    price INTEGER NOT NULL, -- cents
    cost_price INTEGER,
    is_active INTEGER NOT NULL DEFAULT 1,
    FOREIGN KEY (item_id) REFERENCES items (id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_variants_item ON item_variants (item_id);

CREATE TABLE IF NOT EXISTS variant_barcodes (
    barcode TEXT PRIMARY KEY,
    variant_id TEXT NOT NULL,
    barcode_type TEXT DEFAULT 'EAN13',
    is_primary INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (variant_id) REFERENCES item_variants (id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS stock_locations (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE
);

CREATE TABLE IF NOT EXISTS inventory (
    id TEXT PRIMARY KEY,
    item_id TEXT,
    variant_id TEXT,
    location_id TEXT NOT NULL,
    quantity REAL NOT NULL DEFAULT 0, -- REAL because weight items
    reorder_level REAL DEFAULT 0,
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    FOREIGN KEY (item_id) REFERENCES items (id),
    FOREIGN KEY (variant_id) REFERENCES item_variants (id),
    FOREIGN KEY (location_id) REFERENCES stock_locations (id),
    CHECK (
        (
            item_id IS NOT NULL
            AND variant_id IS NULL
        )
        OR (
            item_id IS NULL
            AND variant_id IS NOT NULL
        )
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_inventory_item ON inventory (
    item_id,
    variant_id,
    location_id
);

CREATE TABLE IF NOT EXISTS price_history (
    id TEXT PRIMARY KEY,
    item_id TEXT,
    variant_id TEXT,
    price INTEGER NOT NULL,
    starts_at TEXT NOT NULL DEFAULT (datetime('now')),
    ends_at TEXT,
    FOREIGN KEY (item_id) REFERENCES items (id),
    FOREIGN KEY (variant_id) REFERENCES item_variants (id),
    CHECK (
        (
            item_id IS NOT NULL
            AND variant_id IS NULL
        )
        OR (
            item_id IS NULL
            AND variant_id IS NOT NULL
        )
    )
);

CREATE INDEX IF NOT EXISTS idx_price_history_item ON price_history (item_id, variant_id);

CREATE TABLE IF NOT EXISTS sales (
    id TEXT PRIMARY KEY, -- uuid
    receipt_no TEXT NOT NULL UNIQUE, -- visible receipt number
    status TEXT NOT NULL DEFAULT 'completed', -- open|parked|completed|voided|refunded
    sale_type TEXT NOT NULL DEFAULT 'sale', -- sale|return
    register_id TEXT, -- which till
    cashier_id TEXT, -- user who sold
    customer_id TEXT, -- optional
    currency TEXT NOT NULL DEFAULT 'GBP',
    subtotal INTEGER NOT NULL, -- sum lines before tax/discount
    discount_total INTEGER NOT NULL DEFAULT 0,
    tax_total INTEGER NOT NULL DEFAULT 0,
    total INTEGER NOT NULL, -- final total
    rounding INTEGER NOT NULL DEFAULT 0, -- +/- pennies
    note TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    completed_at TEXT,
    voided_at TEXT,
    FOREIGN KEY (customer_id) REFERENCES customers (id),
    FOREIGN KEY (register_id) REFERENCES registers (id),
    FOREIGN KEY (cashier_id) REFERENCES users (id)
);

CREATE INDEX IF NOT EXISTS idx_sales_created ON sales (created_at);

CREATE INDEX IF NOT EXISTS idx_sales_status ON sales (status);

CREATE TABLE IF NOT EXISTS sale_lines (
    id TEXT PRIMARY KEY,
    sale_id TEXT NOT NULL,
    line_no INTEGER NOT NULL,
    item_id TEXT,
    variant_id TEXT,
    name_snapshot TEXT NOT NULL, -- item name at time of sale
    sku_snapshot TEXT,
    barcode_snapshot TEXT,
    quantity REAL NOT NULL, -- REAL for weighed items
    unit_price INTEGER NOT NULL, -- price before line discount
    line_discount INTEGER NOT NULL DEFAULT 0,
    tax_rate_bp INTEGER NOT NULL, -- VAT rate at time of sale (basis points)
    tax_amount INTEGER NOT NULL,
    total_before_tax INTEGER NOT NULL,
    total_after_tax INTEGER NOT NULL,
    FOREIGN KEY (sale_id) REFERENCES sales (id) ON DELETE CASCADE,
    FOREIGN KEY (item_id) REFERENCES items (id),
    FOREIGN KEY (variant_id) REFERENCES item_variants (id),
    CHECK (
        (
            item_id IS NOT NULL
            AND variant_id IS NULL
        )
        OR (
            item_id IS NULL
            AND variant_id IS NOT NULL
        )
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_sale_lines_sale_line ON sale_lines (sale_id, line_no);

CREATE TABLE IF NOT EXISTS payments (
    id TEXT PRIMARY KEY,
    sale_id TEXT NOT NULL,
    method_id TEXT NOT NULL, -- cash/card/etc
    amount INTEGER NOT NULL, -- paid amount
    currency TEXT NOT NULL DEFAULT 'GBP',
    reference TEXT, -- card auth code, txn id
    change_given INTEGER NOT NULL DEFAULT 0,
    paid_at TEXT NOT NULL DEFAULT (datetime('now')),
    FOREIGN KEY (sale_id) REFERENCES sales (id) ON DELETE CASCADE,
    FOREIGN KEY (method_id) REFERENCES payment_methods (id)
);

CREATE INDEX IF NOT EXISTS idx_payments_sale ON payments (sale_id);

CREATE TABLE IF NOT EXISTS payment_methods (
    id TEXT PRIMARY KEY, -- cash, card, gift, store_credit
    name TEXT NOT NULL UNIQUE,
    type TEXT NOT NULL, -- cash|card|voucher|credit
    is_active INTEGER NOT NULL DEFAULT 1,
    sort_order INTEGER NOT NULL DEFAULT 0
);

INSERT OR IGNORE INTO
    payment_methods (id, name, type, sort_order)
VALUES ('cash', 'Cash', 'cash', 1),
    ('card', 'Card', 'card', 2),
    (
        'gift',
        'Gift Card',
        'voucher',
        3
    );

CREATE TABLE IF NOT EXISTS customers (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    phone TEXT,
    email TEXT,
    address TEXT,
    loyalty_no TEXT UNIQUE,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_customers_phone ON customers (phone);

CREATE TABLE IF NOT EXISTS registers (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE, -- "Front Till"
    location_id TEXT,
    is_active INTEGER NOT NULL DEFAULT 1,
    FOREIGN KEY (location_id) REFERENCES stock_locations (id)
);

CREATE TABLE IF NOT EXISTS users (
    id TEXT PRIMARY KEY,
    username TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    role TEXT NOT NULL DEFAULT 'cashier', -- cashier|manager|admin
    pin_hash TEXT, -- if you want PIN login
    is_active INTEGER NOT NULL DEFAULT 1
);

-- Shifts / cash drawer
-- Tracks opening float, cash drops, end-of-day.

CREATE TABLE IF NOT EXISTS shifts (
    id TEXT PRIMARY KEY,
    register_id TEXT NOT NULL,
    cashier_id TEXT NOT NULL, -- opener
    opened_at TEXT NOT NULL DEFAULT (datetime('now')),
    closed_at TEXT,
    opening_cash INTEGER NOT NULL DEFAULT 0,
    closing_cash INTEGER,
    expected_cash INTEGER,
    note TEXT,
    FOREIGN KEY (register_id) REFERENCES registers (id),
    FOREIGN KEY (cashier_id) REFERENCES users (id)
);

CREATE INDEX IF NOT EXISTS idx_shifts_register_open ON shifts (register_id, opened_at);

CREATE TABLE IF NOT EXISTS sale_discounts (
    id TEXT PRIMARY KEY,
    sale_id TEXT NOT NULL,
    line_id TEXT, -- null means whole-sale discount
    type TEXT NOT NULL, -- percent|fixed
    value INTEGER NOT NULL, -- percent in bp or fixed pennies
    amount INTEGER NOT NULL, -- computed amount applied
    reason TEXT,
    FOREIGN KEY (sale_id) REFERENCES sales (id) ON DELETE CASCADE,
    FOREIGN KEY (line_id) REFERENCES sale_lines (id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_sale_discounts_sale ON sale_discounts (sale_id);

CREATE TABLE IF NOT EXISTS sale_links (
    id TEXT PRIMARY KEY,
    sale_id TEXT NOT NULL, -- the return sale
    original_sale_id TEXT NOT NULL,
    reason TEXT,
    FOREIGN KEY (sale_id) REFERENCES sales (id) ON DELETE CASCADE,
    FOREIGN KEY (original_sale_id) REFERENCES sales (id)
);

CREATE TABLE IF NOT EXISTS stock_movements (
    id TEXT PRIMARY KEY,
    item_id TEXT,
    variant_id TEXT,
    location_id TEXT NOT NULL,
    sale_line_id TEXT, -- nullable for manual adjustments
    type TEXT NOT NULL, -- sale|return|adjust|receive|waste|transfer
    quantity REAL NOT NULL, -- negative for sale, positive for return/receive
    cost_price INTEGER, -- snapshot if needed
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    FOREIGN KEY (item_id) REFERENCES items (id),
    FOREIGN KEY (variant_id) REFERENCES item_variants (id),
    FOREIGN KEY (location_id) REFERENCES stock_locations (id),
    FOREIGN KEY (sale_line_id) REFERENCES sale_lines (id),
    CHECK (
        (
            item_id IS NOT NULL
            AND variant_id IS NULL
        )
        OR (
            item_id IS NULL
            AND variant_id IS NOT NULL
        )
    )
);

CREATE INDEX IF NOT EXISTS idx_stock_movements_item ON stock_movements (
    item_id,
    variant_id,
    created_at
);

CREATE TABLE IF NOT EXISTS audit_log (
    id TEXT PRIMARY KEY,
    actor_id TEXT, -- user who did action
    entity_type TEXT NOT NULL, -- sale|item|price|inventory|shift
    entity_id TEXT NOT NULL,
    action TEXT NOT NULL, -- create|update|void|refund
    data_json TEXT, -- before/after snapshot
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    FOREIGN KEY (actor_id) REFERENCES users (id)
);

CREATE INDEX IF NOT EXISTS idx_audit_entity ON audit_log (entity_type, entity_id);

-- sample items:

PRAGMA foreign_keys = ON;

-- ---------------------------
-- Stock locations
-- ---------------------------
INSERT INTO
    stock_locations (id, name)
VALUES ('loc_main', 'Main Store'),
    ('loc_back', 'Back Store'),
    ('loc_wh', 'Warehouse');

-- ---------------------------
-- Tax codes (basis points)
-- ---------------------------
INSERT INTO
    tax_codes (
        id,
        name,
        rate_basis_points,
        is_active
    )
VALUES (
        'tax_std',
        'Standard VAT',
        2000,
        1
    ),
    (
        'tax_red',
        'Reduced VAT',
        500,
        1
    ),
    (
        'tax_zero',
        'Zero-rated',
        0,
        1
    );

-- ---------------------------
-- Brands
-- ---------------------------
INSERT INTO
    brands (id, name)
VALUES ('br_coca', 'Coca-Cola'),
    ('br_pepsi', 'PepsiCo'),
    ('br_nestle', 'Nestlé'),
    ('br_unilev', 'Unilever'),
    ('br_kell', 'Kellogg''s'),
    ('br_heinz', 'Heinz'),
    ('br_walk', 'Walkers'),
    ('br_generic', 'Generic');

-- ---------------------------
-- Categories (some hierarchy)
-- ---------------------------
INSERT INTO
    categories (
        id,
        name,
        parent_id,
        sort_order
    )
VALUES ('cat_food', 'Food', NULL, 1),
    (
        'cat_drink',
        'Drinks',
        NULL,
        2
    ),
    (
        'cat_snack',
        'Snacks',
        'cat_food',
        1
    ),
    (
        'cat_dairy',
        'Dairy',
        'cat_food',
        2
    ),
    (
        'cat_bakery',
        'Bakery',
        'cat_food',
        3
    ),
    (
        'cat_frozen',
        'Frozen',
        'cat_food',
        4
    ),
    (
        'cat_house',
        'Household',
        NULL,
        3
    ),
    (
        'cat_clean',
        'Cleaning',
        'cat_house',
        1
    ),
    (
        'cat_personal',
        'Personal Care',
        'cat_house',
        2
    ),
    (
        'cat_produce',
        'Produce',
        NULL,
        4
    );

-- ---------------------------
-- 50 Items
-- prices are in pence
-- ---------------------------
INSERT INTO
    items (
        id,
        sku,
        name,
        description,
        category_id,
        brand_id,
        unit,
        base_price,
        cost_price,
        tax_code_id,
        is_active,
        is_weighed
    )
VALUES (
        'itm001',
        'SKU-0001',
        'Coca-Cola Can 330ml',
        'Classic cola can',
        'cat_drink',
        'br_coca',
        'each',
        120,
        60,
        'tax_std',
        1,
        0
    ),
    (
        'itm002',
        'SKU-0002',
        'Pepsi Can 330ml',
        'Cola can',
        'cat_drink',
        'br_pepsi',
        'each',
        115,
        58,
        'tax_std',
        1,
        0
    ),
    (
        'itm003',
        'SKU-0003',
        'Sparkling Water 500ml',
        'Carbonated water',
        'cat_drink',
        'br_generic',
        'each',
        85,
        30,
        'tax_zero',
        1,
        0
    ),
    (
        'itm004',
        'SKU-0004',
        'Still Water 1.5L',
        'Spring water bottle',
        'cat_drink',
        'br_generic',
        'each',
        110,
        40,
        'tax_zero',
        1,
        0
    ),
    (
        'itm005',
        'SKU-0005',
        'Orange Juice 1L',
        '100% orange juice',
        'cat_drink',
        'br_nestle',
        'each',
        220,
        120,
        'tax_zero',
        1,
        0
    ),
    (
        'itm006',
        'SKU-0006',
        'Apple Juice 1L',
        'Pressed apple juice',
        'cat_drink',
        'br_generic',
        'each',
        210,
        110,
        'tax_zero',
        1,
        0
    ),
    (
        'itm007',
        'SKU-0007',
        'Semi-Skimmed Milk 2L',
        'Fresh milk',
        'cat_dairy',
        'br_generic',
        'each',
        190,
        120,
        'tax_zero',
        1,
        0
    ),
    (
        'itm008',
        'SKU-0008',
        'Whole Milk 1L',
        'Fresh milk',
        'cat_dairy',
        'br_generic',
        'each',
        110,
        70,
        'tax_zero',
        1,
        0
    ),
    (
        'itm009',
        'SKU-0009',
        'Butter 250g',
        'Salted butter',
        'cat_dairy',
        'br_unilev',
        'each',
        215,
        140,
        'tax_zero',
        1,
        0
    ),
    (
        'itm010',
        'SKU-0010',
        'Cheddar Cheese 400g',
        'Mature cheddar',
        'cat_dairy',
        'br_generic',
        'each',
        320,
        210,
        'tax_zero',
        1,
        0
    ),
    (
        'itm011',
        'SKU-0011',
        'White Bread Loaf',
        'Sliced loaf',
        'cat_bakery',
        'br_generic',
        'each',
        140,
        80,
        'tax_zero',
        1,
        0
    ),
    (
        'itm012',
        'SKU-0012',
        'Brown Bread Loaf',
        'Wholemeal loaf',
        'cat_bakery',
        'br_generic',
        'each',
        150,
        85,
        'tax_zero',
        1,
        0
    ),
    (
        'itm013',
        'SKU-0013',
        'Croissant Pack x4',
        'Buttery croissants',
        'cat_bakery',
        'br_generic',
        'each',
        200,
        120,
        'tax_zero',
        1,
        0
    ),
    (
        'itm014',
        'SKU-0014',
        'Chocolate Muffin x2',
        'Bakery muffins',
        'cat_bakery',
        'br_generic',
        'each',
        180,
        100,
        'tax_zero',
        1,
        0
    ),
    (
        'itm015',
        'SKU-0015',
        'Walkers Ready Salted 45g',
        'Crisps',
        'cat_snack',
        'br_walk',
        'each',
        95,
        45,
        'tax_std',
        1,
        0
    ),
    (
        'itm016',
        'SKU-0016',
        'Walkers Cheese & Onion 45g',
        'Crisps',
        'cat_snack',
        'br_walk',
        'each',
        95,
        45,
        'tax_std',
        1,
        0
    ),
    (
        'itm017',
        'SKU-0017',
        'Salted Peanuts 200g',
        'Roasted peanuts',
        'cat_snack',
        'br_generic',
        'each',
        170,
        90,
        'tax_std',
        1,
        0
    ),
    (
        'itm018',
        'SKU-0018',
        'Mixed Nuts 200g',
        'Assorted nuts',
        'cat_snack',
        'br_generic',
        'each',
        240,
        135,
        'tax_std',
        1,
        0
    ),
    (
        'itm019',
        'SKU-0019',
        'Milk Chocolate Bar 100g',
        'Chocolate bar',
        'cat_snack',
        'br_nestle',
        'each',
        130,
        70,
        'tax_std',
        1,
        0
    ),
    (
        'itm020',
        'SKU-0020',
        'Dark Chocolate Bar 100g',
        '70% cocoa',
        'cat_snack',
        'br_nestle',
        'each',
        140,
        75,
        'tax_std',
        1,
        0
    ),
    (
        'itm021',
        'SKU-0021',
        'Kellogg''s Cornflakes 500g',
        'Breakfast cereal',
        'cat_food',
        'br_kell',
        'each',
        280,
        160,
        'tax_zero',
        1,
        0
    ),
    (
        'itm022',
        'SKU-0022',
        'Oat Granola 750g',
        'Crunchy oats',
        'cat_food',
        'br_generic',
        'each',
        340,
        200,
        'tax_zero',
        1,
        0
    ),
    (
        'itm023',
        'SKU-0023',
        'Frozen Peas 1kg',
        'Garden peas',
        'cat_frozen',
        'br_generic',
        'each',
        160,
        90,
        'tax_zero',
        1,
        0
    ),
    (
        'itm024',
        'SKU-0024',
        'Frozen Chips 1.5kg',
        'Straight cut fries',
        'cat_frozen',
        'br_generic',
        'each',
        210,
        125,
        'tax_zero',
        1,
        0
    ),
    (
        'itm025',
        'SKU-0025',
        'Vanilla Ice Cream 1L',
        'Ice cream tub',
        'cat_frozen',
        'br_unilev',
        'each',
        350,
        220,
        'tax_zero',
        1,
        0
    ),
    (
        'itm026',
        'SKU-0026',
        'Bananas',
        'Loose bananas per kg',
        'cat_produce',
        'br_generic',
        'kg',
        120,
        70,
        'tax_zero',
        1,
        1
    ),
    (
        'itm027',
        'SKU-0027',
        'Apples',
        'Loose apples per kg',
        'cat_produce',
        'br_generic',
        'kg',
        180,
        95,
        'tax_zero',
        1,
        1
    ),
    (
        'itm028',
        'SKU-0028',
        'Tomatoes',
        'Loose tomatoes per kg',
        'cat_produce',
        'br_generic',
        'kg',
        210,
        120,
        'tax_zero',
        1,
        1
    ),
    (
        'itm029',
        'SKU-0029',
        'Onions',
        'Loose onions per kg',
        'cat_produce',
        'br_generic',
        'kg',
        90,
        50,
        'tax_zero',
        1,
        1
    ),
    (
        'itm030',
        'SKU-0030',
        'Potatoes 2.5kg Bag',
        'Maris Piper',
        'cat_produce',
        'br_generic',
        'each',
        260,
        160,
        'tax_zero',
        1,
        0
    ),
    (
        'itm031',
        'SKU-0031',
        'Heinz Baked Beans 415g',
        'Tinned beans',
        'cat_food',
        'br_heinz',
        'each',
        140,
        80,
        'tax_zero',
        1,
        0
    ),
    (
        'itm032',
        'SKU-0032',
        'Tomato Ketchup 460g',
        'Bottle ketchup',
        'cat_food',
        'br_heinz',
        'each',
        250,
        150,
        'tax_zero',
        1,
        0
    ),
    (
        'itm033',
        'SKU-0033',
        'Pasta Spaghetti 500g',
        'Dry pasta',
        'cat_food',
        'br_generic',
        'each',
        125,
        70,
        'tax_zero',
        1,
        0
    ),
    (
        'itm034',
        'SKU-0034',
        'Rice Basmati 1kg',
        'Long grain rice',
        'cat_food',
        'br_generic',
        'each',
        210,
        130,
        'tax_zero',
        1,
        0
    ),
    (
        'itm035',
        'SKU-0035',
        'Olive Oil 500ml',
        'Extra virgin',
        'cat_food',
        'br_generic',
        'each',
        520,
        350,
        'tax_zero',
        1,
        0
    ),
    (
        'itm036',
        'SKU-0036',
        'Laundry Detergent 1.5L',
        'Color safe',
        'cat_clean',
        'br_unilev',
        'each',
        650,
        420,
        'tax_std',
        1,
        0
    ),
    (
        'itm037',
        'SKU-0037',
        'Dishwashing Liquid 500ml',
        'Lemon scent',
        'cat_clean',
        'br_unilev',
        'each',
        180,
        95,
        'tax_std',
        1,
        0
    ),
    (
        'itm038',
        'SKU-0038',
        'Paper Towels x2',
        'Kitchen roll',
        'cat_house',
        'br_generic',
        'each',
        220,
        120,
        'tax_std',
        1,
        0
    ),
    (
        'itm039',
        'SKU-0039',
        'Toilet Paper x9',
        'Soft tissue',
        'cat_house',
        'br_generic',
        'each',
        590,
        400,
        'tax_std',
        1,
        0
    ),
    (
        'itm040',
        'SKU-0040',
        'All-Purpose Cleaner 750ml',
        'Spray cleaner',
        'cat_clean',
        'br_generic',
        'each',
        240,
        135,
        'tax_std',
        1,
        0
    ),
    (
        'itm041',
        'SKU-0041',
        'Shampoo 400ml',
        'Daily care shampoo',
        'cat_personal',
        'br_unilev',
        'each',
        310,
        190,
        'tax_std',
        1,
        0
    ),
    (
        'itm042',
        'SKU-0042',
        'Conditioner 400ml',
        'Moisture conditioner',
        'cat_personal',
        'br_unilev',
        'each',
        310,
        190,
        'tax_std',
        1,
        0
    ),
    (
        'itm043',
        'SKU-0043',
        'Soap Bar 2x100g',
        'Bath soap',
        'cat_personal',
        'br_unilev',
        'each',
        160,
        90,
        'tax_std',
        1,
        0
    ),
    (
        'itm044',
        'SKU-0044',
        'Toothpaste 75ml',
        'Mint toothpaste',
        'cat_personal',
        'br_generic',
        'each',
        210,
        120,
        'tax_std',
        1,
        0
    ),
    (
        'itm045',
        'SKU-0045',
        'Toothbrush Medium',
        'Manual brush',
        'cat_personal',
        'br_generic',
        'each',
        140,
        70,
        'tax_std',
        1,
        0
    ),
    (
        'itm046',
        'SKU-0046',
        'Energy Drink 250ml',
        'Caffeinated drink',
        'cat_drink',
        'br_generic',
        'each',
        155,
        80,
        'tax_std',
        1,
        0
    ),
    (
        'itm047',
        'SKU-0047',
        'Protein Bar 60g',
        'Chocolate protein bar',
        'cat_snack',
        'br_generic',
        'each',
        210,
        120,
        'tax_std',
        1,
        0
    ),
    (
        'itm048',
        'SKU-0048',
        'Instant Coffee 200g',
        'Freeze dried coffee',
        'cat_drink',
        'br_nestle',
        'each',
        460,
        310,
        'tax_zero',
        1,
        0
    ),
    (
        'itm049',
        'SKU-0049',
        'Tea Bags x80',
        'Black tea',
        'cat_drink',
        'br_generic',
        'each',
        295,
        180,
        'tax_zero',
        1,
        0
    ),
    (
        'itm050',
        'SKU-0050',
        'Sugar 1kg',
        'Granulated sugar',
        'cat_food',
        'br_generic',
        'each',
        135,
        75,
        'tax_zero',
        1,
        0
    );

-- ---------------------------
-- Item barcodes (1 each)
-- ---------------------------
INSERT INTO
    item_barcodes (
        barcode,
        item_id,
        barcode_type,
        is_primary
    )
VALUES (
        '5000000000011',
        'itm001',
        'EAN13',
        1
    ),
    (
        '5000000000028',
        'itm002',
        'EAN13',
        1
    ),
    (
        '5000000000035',
        'itm003',
        'EAN13',
        1
    ),
    (
        '5000000000042',
        'itm004',
        'EAN13',
        1
    ),
    (
        '5000000000059',
        'itm005',
        'EAN13',
        1
    ),
    (
        '5000000000066',
        'itm006',
        'EAN13',
        1
    ),
    (
        '5000000000073',
        'itm007',
        'EAN13',
        1
    ),
    (
        '5000000000080',
        'itm008',
        'EAN13',
        1
    ),
    (
        '5000000000097',
        'itm009',
        'EAN13',
        1
    ),
    (
        '5000000000103',
        'itm010',
        'EAN13',
        1
    ),
    (
        '5000000000110',
        'itm011',
        'EAN13',
        1
    ),
    (
        '5000000000127',
        'itm012',
        'EAN13',
        1
    ),
    (
        '5000000000134',
        'itm013',
        'EAN13',
        1
    ),
    (
        '5000000000141',
        'itm014',
        'EAN13',
        1
    ),
    (
        '5000000000158',
        'itm015',
        'EAN13',
        1
    ),
    (
        '5000000000165',
        'itm016',
        'EAN13',
        1
    ),
    (
        '5000000000172',
        'itm017',
        'EAN13',
        1
    ),
    (
        '5000000000189',
        'itm018',
        'EAN13',
        1
    ),
    (
        '5000000000196',
        'itm019',
        'EAN13',
        1
    ),
    (
        '5000000000202',
        'itm020',
        'EAN13',
        1
    ),
    (
        '5000000000219',
        'itm021',
        'EAN13',
        1
    ),
    (
        '5000000000226',
        'itm022',
        'EAN13',
        1
    ),
    (
        '5000000000233',
        'itm023',
        'EAN13',
        1
    ),
    (
        '5000000000240',
        'itm024',
        'EAN13',
        1
    ),
    (
        '5000000000257',
        'itm025',
        'EAN13',
        1
    ),
    (
        '5000000000264',
        'itm026',
        'EAN13',
        1
    ),
    (
        '5000000000271',
        'itm027',
        'EAN13',
        1
    ),
    (
        '5000000000288',
        'itm028',
        'EAN13',
        1
    ),
    (
        '5000000000295',
        'itm029',
        'EAN13',
        1
    ),
    (
        '5000000000301',
        'itm030',
        'EAN13',
        1
    ),
    (
        '5000000000318',
        'itm031',
        'EAN13',
        1
    ),
    (
        '5000000000325',
        'itm032',
        'EAN13',
        1
    ),
    (
        '5000000000332',
        'itm033',
        'EAN13',
        1
    ),
    (
        '5000000000349',
        'itm034',
        'EAN13',
        1
    ),
    (
        '5000000000356',
        'itm035',
        'EAN13',
        1
    ),
    (
        '5000000000363',
        'itm036',
        'EAN13',
        1
    ),
    (
        '5000000000370',
        'itm037',
        'EAN13',
        1
    ),
    (
        '5000000000387',
        'itm038',
        'EAN13',
        1
    ),
    (
        '5000000000394',
        'itm039',
        'EAN13',
        1
    ),
    (
        '5000000000400',
        'itm040',
        'EAN13',
        1
    ),
    (
        '5000000000417',
        'itm041',
        'EAN13',
        1
    ),
    (
        '5000000000424',
        'itm042',
        'EAN13',
        1
    ),
    (
        '5000000000431',
        'itm043',
        'EAN13',
        1
    ),
    (
        '5000000000448',
        'itm044',
        'EAN13',
        1
    ),
    (
        '5000000000455',
        'itm045',
        'EAN13',
        1
    ),
    (
        '5000000000462',
        'itm046',
        'EAN13',
        1
    ),
    (
        '5000000000479',
        'itm047',
        'EAN13',
        1
    ),
    (
        '5000000000486',
        'itm048',
        'EAN13',
        1
    ),
    (
        '5000000000493',
        'itm049',
        'EAN13',
        1
    ),
    (
        '5000000000509',
        'itm050',
        'EAN13',
        1
    );

-- ---------------------------
-- Item images (thumbnail paths)
-- ---------------------------
INSERT INTO
    item_images (
        id,
        item_id,
        role,
        path,
        sort_order
    )
VALUES (
        'img001',
        'itm001',
        'thumbnail',
        '/data/assets/items/itm001/thumb.png',
        0
    ),
    (
        'img002',
        'itm002',
        'thumbnail',
        '/data/assets/items/itm002/thumb.png',
        0
    ),
    (
        'img003',
        'itm003',
        'thumbnail',
        '/data/assets/items/itm003/thumb.png',
        0
    ),
    (
        'img004',
        'itm004',
        'thumbnail',
        '/data/assets/items/itm004/thumb.png',
        0
    ),
    (
        'img005',
        'itm005',
        'thumbnail',
        '/data/assets/items/itm005/thumb.png',
        0
    ),
    (
        'img006',
        'itm006',
        'thumbnail',
        '/data/assets/items/itm006/thumb.png',
        0
    ),
    (
        'img007',
        'itm007',
        'thumbnail',
        '/data/assets/items/itm007/thumb.png',
        0
    ),
    (
        'img008',
        'itm008',
        'thumbnail',
        '/data/assets/items/itm008/thumb.png',
        0
    ),
    (
        'img009',
        'itm009',
        'thumbnail',
        '/data/assets/items/itm009/thumb.png',
        0
    ),
    (
        'img010',
        'itm010',
        'thumbnail',
        '/data/assets/items/itm010/thumb.png',
        0
    ),
    (
        'img011',
        'itm011',
        'thumbnail',
        '/data/assets/items/itm011/thumb.png',
        0
    ),
    (
        'img012',
        'itm012',
        'thumbnail',
        '/data/assets/items/itm012/thumb.png',
        0
    ),
    (
        'img013',
        'itm013',
        'thumbnail',
        '/data/assets/items/itm013/thumb.png',
        0
    ),
    (
        'img014',
        'itm014',
        'thumbnail',
        '/data/assets/items/itm014/thumb.png',
        0
    ),
    (
        'img015',
        'itm015',
        'thumbnail',
        '/data/assets/items/itm015/thumb.png',
        0
    ),
    (
        'img016',
        'itm016',
        'thumbnail',
        '/data/assets/items/itm016/thumb.png',
        0
    ),
    (
        'img017',
        'itm017',
        'thumbnail',
        '/data/assets/items/itm017/thumb.png',
        0
    ),
    (
        'img018',
        'itm018',
        'thumbnail',
        '/data/assets/items/itm018/thumb.png',
        0
    ),
    (
        'img019',
        'itm019',
        'thumbnail',
        '/data/assets/items/itm019/thumb.png',
        0
    ),
    (
        'img020',
        'itm020',
        'thumbnail',
        '/data/assets/items/itm020/thumb.png',
        0
    ),
    (
        'img021',
        'itm021',
        'thumbnail',
        '/data/assets/items/itm021/thumb.png',
        0
    ),
    (
        'img022',
        'itm022',
        'thumbnail',
        '/data/assets/items/itm022/thumb.png',
        0
    ),
    (
        'img023',
        'itm023',
        'thumbnail',
        '/data/assets/items/itm023/thumb.png',
        0
    ),
    (
        'img024',
        'itm024',
        'thumbnail',
        '/data/assets/items/itm024/thumb.png',
        0
    ),
    (
        'img025',
        'itm025',
        'thumbnail',
        '/data/assets/items/itm025/thumb.png',
        0
    ),
    (
        'img026',
        'itm026',
        'thumbnail',
        '/data/assets/items/itm026/thumb.png',
        0
    ),
    (
        'img027',
        'itm027',
        'thumbnail',
        '/data/assets/items/itm027/thumb.png',
        0
    ),
    (
        'img028',
        'itm028',
        'thumbnail',
        '/data/assets/items/itm028/thumb.png',
        0
    ),
    (
        'img029',
        'itm029',
        'thumbnail',
        '/data/assets/items/itm029/thumb.png',
        0
    ),
    (
        'img030',
        'itm030',
        'thumbnail',
        '/data/assets/items/itm030/thumb.png',
        0
    ),
    (
        'img031',
        'itm031',
        'thumbnail',
        '/data/assets/items/itm031/thumb.png',
        0
    ),
    (
        'img032',
        'itm032',
        'thumbnail',
        '/data/assets/items/itm032/thumb.png',
        0
    ),
    (
        'img033',
        'itm033',
        'thumbnail',
        '/data/assets/items/itm033/thumb.png',
        0
    ),
    (
        'img034',
        'itm034',
        'thumbnail',
        '/data/assets/items/itm034/thumb.png',
        0
    ),
    (
        'img035',
        'itm035',
        'thumbnail',
        '/data/assets/items/itm035/thumb.png',
        0
    ),
    (
        'img036',
        'itm036',
        'thumbnail',
        '/data/assets/items/itm036/thumb.png',
        0
    ),
    (
        'img037',
        'itm037',
        'thumbnail',
        '/data/assets/items/itm037/thumb.png',
        0
    ),
    (
        'img038',
        'itm038',
        'thumbnail',
        '/data/assets/items/itm038/thumb.png',
        0
    ),
    (
        'img039',
        'itm039',
        'thumbnail',
        '/data/assets/items/itm039/thumb.png',
        0
    ),
    (
        'img040',
        'itm040',
        'thumbnail',
        '/data/assets/items/itm040/thumb.png',
        0
    ),
    (
        'img041',
        'itm041',
        'thumbnail',
        '/data/assets/items/itm041/thumb.png',
        0
    ),
    (
        'img042',
        'itm042',
        'thumbnail',
        '/data/assets/items/itm042/thumb.png',
        0
    ),
    (
        'img043',
        'itm043',
        'thumbnail',
        '/data/assets/items/itm043/thumb.png',
        0
    ),
    (
        'img044',
        'itm044',
        'thumbnail',
        '/data/assets/items/itm044/thumb.png',
        0
    ),
    (
        'img045',
        'itm045',
        'thumbnail',
        '/data/assets/items/itm045/thumb.png',
        0
    ),
    (
        'img046',
        'itm046',
        'thumbnail',
        '/data/assets/items/itm046/thumb.png',
        0
    ),
    (
        'img047',
        'itm047',
        'thumbnail',
        '/data/assets/items/itm047/thumb.png',
        0
    ),
    (
        'img048',
        'itm048',
        'thumbnail',
        '/data/assets/items/itm048/thumb.png',
        0
    ),
    (
        'img049',
        'itm049',
        'thumbnail',
        '/data/assets/items/itm049/thumb.png',
        0
    ),
    (
        'img050',
        'itm050',
        'thumbnail',
        '/data/assets/items/itm050/thumb.png',
        0
    );

-- ---------------------------
-- Inventory (Main Store)
-- quantity REAL supports weighted items
-- ---------------------------
INSERT INTO
    inventory (
        id,
        item_id,
        variant_id,
        location_id,
        quantity,
        reorder_level
    )
VALUES (
        'inv001',
        'itm001',
        NULL,
        'loc_main',
        40,
        10
    ),
    (
        'inv002',
        'itm002',
        NULL,
        'loc_main',
        38,
        10
    ),
    (
        'inv003',
        'itm003',
        NULL,
        'loc_main',
        55,
        12
    ),
    (
        'inv004',
        'itm004',
        NULL,
        'loc_main',
        35,
        8
    ),
    (
        'inv005',
        'itm005',
        NULL,
        'loc_main',
        20,
        5
    ),
    (
        'inv006',
        'itm006',
        NULL,
        'loc_main',
        18,
        5
    ),
    (
        'inv007',
        'itm007',
        NULL,
        'loc_main',
        25,
        6
    ),
    (
        'inv008',
        'itm008',
        NULL,
        'loc_main',
        30,
        6
    ),
    (
        'inv009',
        'itm009',
        NULL,
        'loc_main',
        22,
        4
    ),
    (
        'inv010',
        'itm010',
        NULL,
        'loc_main',
        15,
        4
    ),
    (
        'inv011',
        'itm011',
        NULL,
        'loc_main',
        28,
        6
    ),
    (
        'inv012',
        'itm012',
        NULL,
        'loc_main',
        24,
        6
    ),
    (
        'inv013',
        'itm013',
        NULL,
        'loc_main',
        12,
        3
    ),
    (
        'inv014',
        'itm014',
        NULL,
        'loc_main',
        14,
        3
    ),
    (
        'inv015',
        'itm015',
        NULL,
        'loc_main',
        60,
        15
    ),
    (
        'inv016',
        'itm016',
        NULL,
        'loc_main',
        58,
        15
    ),
    (
        'inv017',
        'itm017',
        NULL,
        'loc_main',
        26,
        6
    ),
    (
        'inv018',
        'itm018',
        NULL,
        'loc_main',
        19,
        5
    ),
    (
        'inv019',
        'itm019',
        NULL,
        'loc_main',
        42,
        10
    ),
    (
        'inv020',
        'itm020',
        NULL,
        'loc_main',
        40,
        10
    ),
    (
        'inv021',
        'itm021',
        NULL,
        'loc_main',
        17,
        4
    ),
    (
        'inv022',
        'itm022',
        NULL,
        'loc_main',
        14,
        4
    ),
    (
        'inv023',
        'itm023',
        NULL,
        'loc_main',
        23,
        5
    ),
    (
        'inv024',
        'itm024',
        NULL,
        'loc_main',
        16,
        4
    ),
    (
        'inv025',
        'itm025',
        NULL,
        'loc_main',
        11,
        3
    ),
    (
        'inv026',
        'itm026',
        NULL,
        'loc_main',
        52.5,
        10
    ),
    (
        'inv027',
        'itm027',
        NULL,
        'loc_main',
        33.0,
        8
    ),
    (
        'inv028',
        'itm028',
        NULL,
        'loc_main',
        27.0,
        6
    ),
    (
        'inv029',
        'itm029',
        NULL,
        'loc_main',
        45.0,
        9
    ),
    (
        'inv030',
        'itm030',
        NULL,
        'loc_main',
        20,
        5
    ),
    (
        'inv031',
        'itm031',
        NULL,
        'loc_main',
        36,
        8
    ),
    (
        'inv032',
        'itm032',
        NULL,
        'loc_main',
        14,
        3
    ),
    (
        'inv033',
        'itm033',
        NULL,
        'loc_main',
        32,
        7
    ),
    (
        'inv034',
        'itm034',
        NULL,
        'loc_main',
        21,
        5
    ),
    (
        'inv035',
        'itm035',
        NULL,
        'loc_main',
        9,
        2
    ),
    (
        'inv036',
        'itm036',
        NULL,
        'loc_main',
        10,
        2
    ),
    (
        'inv037',
        'itm037',
        NULL,
        'loc_main',
        18,
        4
    ),
    (
        'inv038',
        'itm038',
        NULL,
        'loc_main',
        16,
        4
    ),
    (
        'inv039',
        'itm039',
        NULL,
        'loc_main',
        12,
        3
    ),
    (
        'inv040',
        'itm040',
        NULL,
        'loc_main',
        14,
        3
    ),
    (
        'inv041',
        'itm041',
        NULL,
        'loc_main',
        13,
        3
    ),
    (
        'inv042',
        'itm042',
        NULL,
        'loc_main',
        12,
        3
    ),
    (
        'inv043',
        'itm043',
        NULL,
        'loc_main',
        22,
        5
    ),
    (
        'inv044',
        'itm044',
        NULL,
        'loc_main',
        20,
        5
    ),
    (
        'inv045',
        'itm045',
        NULL,
        'loc_main',
        24,
        5
    ),
    (
        'inv046',
        'itm046',
        NULL,
        'loc_main',
        30,
        7
    ),
    (
        'inv047',
        'itm047',
        NULL,
        'loc_main',
        26,
        6
    ),
    (
        'inv048',
        'itm048',
        NULL,
        'loc_main',
        8,
        2
    ),
    (
        'inv049',
        'itm049',
        NULL,
        'loc_main',
        11,
        2
    ),
    (
        'inv050',
        'itm050',
        NULL,
        'loc_main',
        28,
        6
    );

-- ---------------------------
-- Price history (one current row each)
-- ---------------------------
INSERT INTO
    price_history (
        id,
        item_id,
        variant_id,
        price,
        starts_at,
        ends_at
    )
VALUES (
        'ph001',
        'itm001',
        NULL,
        120,
        '2025-01-01',
        NULL
    ),
    (
        'ph002',
        'itm002',
        NULL,
        115,
        '2025-01-01',
        NULL
    ),
    (
        'ph003',
        'itm003',
        NULL,
        85,
        '2025-01-01',
        NULL
    ),
    (
        'ph004',
        'itm004',
        NULL,
        110,
        '2025-01-01',
        NULL
    ),
    (
        'ph005',
        'itm005',
        NULL,
        220,
        '2025-01-01',
        NULL
    ),
    (
        'ph006',
        'itm006',
        NULL,
        210,
        '2025-01-01',
        NULL
    ),
    (
        'ph007',
        'itm007',
        NULL,
        190,
        '2025-01-01',
        NULL
    ),
    (
        'ph008',
        'itm008',
        NULL,
        110,
        '2025-01-01',
        NULL
    ),
    (
        'ph009',
        'itm009',
        NULL,
        215,
        '2025-01-01',
        NULL
    ),
    (
        'ph010',
        'itm010',
        NULL,
        320,
        '2025-01-01',
        NULL
    ),
    (
        'ph011',
        'itm011',
        NULL,
        140,
        '2025-01-01',
        NULL
    ),
    (
        'ph012',
        'itm012',
        NULL,
        150,
        '2025-01-01',
        NULL
    ),
    (
        'ph013',
        'itm013',
        NULL,
        200,
        '2025-01-01',
        NULL
    ),
    (
        'ph014',
        'itm014',
        NULL,
        180,
        '2025-01-01',
        NULL
    ),
    (
        'ph015',
        'itm015',
        NULL,
        95,
        '2025-01-01',
        NULL
    ),
    (
        'ph016',
        'itm016',
        NULL,
        95,
        '2025-01-01',
        NULL
    ),
    (
        'ph017',
        'itm017',
        NULL,
        170,
        '2025-01-01',
        NULL
    ),
    (
        'ph018',
        'itm018',
        NULL,
        240,
        '2025-01-01',
        NULL
    ),
    (
        'ph019',
        'itm019',
        NULL,
        130,
        '2025-01-01',
        NULL
    ),
    (
        'ph020',
        'itm020',
        NULL,
        140,
        '2025-01-01',
        NULL
    ),
    (
        'ph021',
        'itm021',
        NULL,
        280,
        '2025-01-01',
        NULL
    ),
    (
        'ph022',
        'itm022',
        NULL,
        340,
        '2025-01-01',
        NULL
    ),
    (
        'ph023',
        'itm023',
        NULL,
        160,
        '2025-01-01',
        NULL
    ),
    (
        'ph024',
        'itm024',
        NULL,
        210,
        '2025-01-01',
        NULL
    ),
    (
        'ph025',
        'itm025',
        NULL,
        350,
        '2025-01-01',
        NULL
    ),
    (
        'ph026',
        'itm026',
        NULL,
        120,
        '2025-01-01',
        NULL
    ),
    (
        'ph027',
        'itm027',
        NULL,
        180,
        '2025-01-01',
        NULL
    ),
    (
        'ph028',
        'itm028',
        NULL,
        210,
        '2025-01-01',
        NULL
    ),
    (
        'ph029',
        'itm029',
        NULL,
        90,
        '2025-01-01',
        NULL
    ),
    (
        'ph030',
        'itm030',
        NULL,
        260,
        '2025-01-01',
        NULL
    ),
    (
        'ph031',
        'itm031',
        NULL,
        140,
        '2025-01-01',
        NULL
    ),
    (
        'ph032',
        'itm032',
        NULL,
        250,
        '2025-01-01',
        NULL
    ),
    (
        'ph033',
        'itm033',
        NULL,
        125,
        '2025-01-01',
        NULL
    ),
    (
        'ph034',
        'itm034',
        NULL,
        210,
        '2025-01-01',
        NULL
    ),
    (
        'ph035',
        'itm035',
        NULL,
        520,
        '2025-01-01',
        NULL
    ),
    (
        'ph036',
        'itm036',
        NULL,
        650,
        '2025-01-01',
        NULL
    ),
    (
        'ph037',
        'itm037',
        NULL,
        180,
        '2025-01-01',
        NULL
    ),
    (
        'ph038',
        'itm038',
        NULL,
        220,
        '2025-01-01',
        NULL
    ),
    (
        'ph039',
        'itm039',
        NULL,
        590,
        '2025-01-01',
        NULL
    ),
    (
        'ph040',
        'itm040',
        NULL,
        240,
        '2025-01-01',
        NULL
    ),
    (
        'ph041',
        'itm041',
        NULL,
        310,
        '2025-01-01',
        NULL
    ),
    (
        'ph042',
        'itm042',
        NULL,
        310,
        '2025-01-01',
        NULL
    ),
    (
        'ph043',
        'itm043',
        NULL,
        160,
        '2025-01-01',
        NULL
    ),
    (
        'ph044',
        'itm044',
        NULL,
        210,
        '2025-01-01',
        NULL
    ),
    (
        'ph045',
        'itm045',
        NULL,
        140,
        '2025-01-01',
        NULL
    ),
    (
        'ph046',
        'itm046',
        NULL,
        155,
        '2025-01-01',
        NULL
    ),
    (
        'ph047',
        'itm047',
        NULL,
        210,
        '2025-01-01',
        NULL
    ),
    (
        'ph048',
        'itm048',
        NULL,
        460,
        '2025-01-01',
        NULL
    ),
    (
        'ph049',
        'itm049',
        NULL,
        295,
        '2025-01-01',
        NULL
    ),
    (
        'ph050',
        'itm050',
        NULL,
        135,
        '2025-01-01',
        NULL
    );

-- ---------------------------
-- Variants for a subset (12 items)
-- Each variant is sellable, with its own price & barcode
-- ---------------------------

INSERT INTO
    item_variants (
        id,
        item_id,
        sku,
        name,
        price,
        cost_price,
        is_active
    )
VALUES (
        'var001',
        'itm001',
        'SKU-0001-6P',
        'Pack of 6',
        650,
        330,
        1
    ),
    (
        'var002',
        'itm001',
        'SKU-0001-12P',
        'Pack of 12',
        1200,
        640,
        1
    ),
    (
        'var003',
        'itm002',
        'SKU-0002-6P',
        'Pack of 6',
        630,
        320,
        1
    ),
    (
        'var004',
        'itm005',
        'SKU-0005-500',
        '500ml Bottle',
        140,
        75,
        1
    ),
    (
        'var005',
        'itm005',
        'SKU-0005-1L',
        '1L Bottle',
        220,
        120,
        1
    ),
    (
        'var006',
        'itm015',
        'SKU-0015-MP',
        'Multipack x6',
        520,
        260,
        1
    ),
    (
        'var007',
        'itm021',
        'SKU-0021-1K',
        '1kg Family Box',
        420,
        250,
        1
    ),
    (
        'var008',
        'itm024',
        'SKU-0024-1K',
        '1kg Bag',
        160,
        90,
        1
    ),
    (
        'var009',
        'itm024',
        'SKU-0024-2K',
        '2kg Bag',
        280,
        160,
        1
    ),
    (
        'var010',
        'itm041',
        'SKU-0041-250',
        '250ml Bottle',
        210,
        120,
        1
    ),
    (
        'var011',
        'itm041',
        'SKU-0041-400',
        '400ml Bottle',
        310,
        190,
        1
    ),
    (
        'var012',
        'itm046',
        'SKU-0046-4P',
        '4 Pack',
        540,
        280,
        1
    );

INSERT INTO
    variant_barcodes (
        barcode,
        variant_id,
        barcode_type,
        is_primary
    )
VALUES (
        '6000000000018',
        'var001',
        'EAN13',
        1
    ),
    (
        '6000000000025',
        'var002',
        'EAN13',
        1
    ),
    (
        '6000000000032',
        'var003',
        'EAN13',
        1
    ),
    (
        '6000000000049',
        'var004',
        'EAN13',
        1
    ),
    (
        '6000000000056',
        'var005',
        'EAN13',
        1
    ),
    (
        '6000000000063',
        'var006',
        'EAN13',
        1
    ),
    (
        '6000000000070',
        'var007',
        'EAN13',
        1
    ),
    (
        '6000000000087',
        'var008',
        'EAN13',
        1
    ),
    (
        '6000000000094',
        'var009',
        'EAN13',
        1
    ),
    (
        '6000000000100',
        'var010',
        'EAN13',
        1
    ),
    (
        '6000000000117',
        'var011',
        'EAN13',
        1
    ),
    (
        '6000000000124',
        'var012',
        'EAN13',
        1
    );

-- Inventory for variants (Main Store)
INSERT INTO
    inventory (
        id,
        item_id,
        variant_id,
        location_id,
        quantity,
        reorder_level
    )
VALUES (
        'inv051',
        NULL,
        'var001',
        'loc_main',
        12,
        3
    ),
    (
        'inv052',
        NULL,
        'var002',
        'loc_main',
        8,
        2
    ),
    (
        'inv053',
        NULL,
        'var003',
        'loc_main',
        10,
        2
    ),
    (
        'inv054',
        NULL,
        'var004',
        'loc_main',
        20,
        5
    ),
    (
        'inv055',
        NULL,
        'var005',
        'loc_main',
        15,
        4
    ),
    (
        'inv056',
        NULL,
        'var006',
        'loc_main',
        14,
        3
    ),
    (
        'inv057',
        NULL,
        'var007',
        'loc_main',
        7,
        2
    ),
    (
        'inv058',
        NULL,
        'var008',
        'loc_main',
        9,
        2
    ),
    (
        'inv059',
        NULL,
        'var009',
        'loc_main',
        6,
        2
    ),
    (
        'inv060',
        NULL,
        'var010',
        'loc_main',
        11,
        3
    ),
    (
        'inv061',
        NULL,
        'var011',
        'loc_main',
        9,
        2
    ),
    (
        'inv062',
        NULL,
        'var012',
        'loc_main',
        13,
        3
    );

-- Price history for variants
INSERT INTO
    price_history (
        id,
        item_id,
        variant_id,
        price,
        starts_at,
        ends_at
    )
VALUES (
        'ph051',
        NULL,
        'var001',
        650,
        '2025-01-01',
        NULL
    ),
    (
        'ph052',
        NULL,
        'var002',
        1200,
        '2025-01-01',
        NULL
    ),
    (
        'ph053',
        NULL,
        'var003',
        630,
        '2025-01-01',
        NULL
    ),
    (
        'ph054',
        NULL,
        'var004',
        140,
        '2025-01-01',
        NULL
    ),
    (
        'ph055',
        NULL,
        'var005',
        220,
        '2025-01-01',
        NULL
    ),
    (
        'ph056',
        NULL,
        'var006',
        520,
        '2025-01-01',
        NULL
    ),
    (
        'ph057',
        NULL,
        'var007',
        420,
        '2025-01-01',
        NULL
    ),
    (
        'ph058',
        NULL,
        'var008',
        160,
        '2025-01-01',
        NULL
    ),
    (
        'ph059',
        NULL,
        'var009',
        280,
        '2025-01-01',
        NULL
    ),
    (
        'ph060',
        NULL,
        'var010',
        210,
        '2025-01-01',
        NULL
    ),
    (
        'ph061',
        NULL,
        'var011',
        310,
        '2025-01-01',
        NULL
    ),
    (
        'ph062',
        NULL,
        'var012',
        540,
        '2025-01-01',
        NULL
    );

-- some defaults
INSERT OR IGNORE INTO
    settings (key, value)
VALUES ('store.name', 'My Store'),
    ('store.currency', 'GBP'),
    ('pos.tax_inclusive', 'false');