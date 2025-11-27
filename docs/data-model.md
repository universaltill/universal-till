# UniversalTill Data Model (SQLite)

This document describes the physical relational schema implemented by `001_init.sql`
and subsequent migrations.

- DB engine: SQLite
- Migrations: `internal/db/migrations/*.sql`
- Foreign keys: `PRAGMA foreign_keys = ON`

## Money & quantities

- All monetary values are stored as INTEGER minor units (pence).
- Quantities:
  - `REAL` is used where items can be weighed (`unit = 'kg'` and `is_weighed = 1`).
  - Otherwise they represent whole units.

## Table groups

### 1. Lookup & configuration

- `schema_migrations`
- `settings`
- `tax_codes`
- `categories`
- `brands`
- `stock_locations`

(then briefly describe each, you can copy from the constitution section above)

### 2. Catalog

- `items`
- `item_barcodes`
- `item_images`
- `item_variants`
- `variant_barcodes`

### 3. Inventory & pricing

- `inventory`
- `price_history`
- `stock_movements`

### 4. Sales & payments

- `sales`
- `sale_lines`
- `sale_discounts`
- `payments`
- `sale_links`

### 5. Shifts & audit

- `shifts`
- `audit_log`

### 6. Parties & tills

- `customers`
- `users`
- `registers`

### 7. Plugins & shortcuts

- `plugin_catalog`
- `plugins`
- `plugin_entries`
- `plugin_settings`
- `plugin_hooks`
- `plugin_permissions`
- `shortcut_buttons`

## ER Diagram

# 1️⃣ Core POS – High-level Overview

```mermaid
erDiagram

    items {}
    item_variants {}
    item_barcodes {}
    item_images {}
    variant_barcodes {}
    tax_codes {}
    categories {}
    brands {}
    stock_locations {}
    inventory {}
    price_history {}

    customers {}
    users {}
    registers {}
    payment_methods {}

    sales {}
    sale_lines {}
    payments {}
    sale_discounts {}
    sale_links {}
    shifts {}
    stock_movements {}
    audit_log {}

    plugin_catalog {}
    plugins {}
    plugin_entries {}
    plugin_settings {}
    plugin_hooks {}
    plugin_permissions {}

    shortcut_buttons {}

    %% Catalog / lookup
    categories ||--o{ categories       : "parent_of"
    categories ||--o{ items            : "category"
    brands     ||--o{ items            : "brand"
    tax_codes  ||--o{ items            : "tax_code"

    items         ||--o{ item_barcodes : "has"
    items         ||--o{ item_images   : "has"
    items         ||--o{ item_variants : "has"
    item_variants ||--o{ variant_barcodes : "has"

    %% Inventory & pricing
    stock_locations ||--o{ inventory   : "stock_at"
    items           ||--o{ inventory   : "stock_for_item"
    item_variants   ||--o{ inventory   : "stock_for_variant"
    items           ||--o{ price_history : "price_for_item"
    item_variants   ||--o{ price_history : "price_for_variant"

    %% Sales & payments
    customers ||--o{ sales             : "places"
    registers ||--o{ sales             : "records"
    users     ||--o{ sales             : "handles"

    sales     ||--o{ sale_lines        : "has_lines"
    sales     ||--o{ payments          : "has_payments"
    sales     ||--o{ sale_discounts    : "has_discounts"
    sales     ||--o{ sale_links        : "links"

    sale_lines ||--o{ sale_discounts   : "line_discounts"
    payment_methods ||--o{ payments    : "via"

    %% Shifts & stock
    registers ||--o{ shifts            : "runs"
    users     ||--o{ shifts            : "opens"

    items         ||--o{ stock_movements : "moved_as_item"
    item_variants ||--o{ stock_movements : "moved_as_variant"
    stock_locations ||--o{ stock_movements : "moves_at"
    sale_lines    ||--o{ stock_movements : "from_line"

    %% Audit
    users ||--o{ audit_log            : "acts_in"

    %% Plugins & shortcuts
    plugin_catalog ||--o{ plugins           : "versions"
    plugins        ||--o{ plugin_entries    : "exposes"
    plugins        ||--o{ plugin_settings   : "configured_by"
    plugins        ||--o{ plugin_hooks      : "hooks"
    plugins        ||--o{ plugin_permissions: "requires"

    items ||--o{ shortcut_buttons      : "shortcut_for"

```
---
# 2️⃣ Catalog & Inventory – Rich Detail

```mermaid
erDiagram

    tax_codes {
        string id
        string name
        int    rate_basis_points
        boolean is_active
    }

    categories {
        string id
        string name
        string parent_id
        int    sort_order
    }

    brands {
        string id
        string name
    }

    items {
        string id
        string sku
        string name
        string description
        string category_id
        string brand_id
        string unit
        int    base_price
        int    cost_price
        string tax_code_id
        boolean is_active
        boolean is_weighed
        string created_at
        string updated_at
    }

    item_barcodes {
        string barcode
        string item_id
        string barcode_type
        boolean is_primary
    }

    item_images {
        string id
        string item_id
        string role
        string path
        int    sort_order
    }

    item_variants {
        string id
        string item_id
        string sku
        string name
        int    price
        int    cost_price
        boolean is_active
    }

    variant_barcodes {
        string barcode
        string variant_id
        string barcode_type
        boolean is_primary
    }

    stock_locations {
        string id
        string name
    }

    inventory {
        string id
        string item_id
        string variant_id
        string location_id
        float  quantity
        float  reorder_level
        string updated_at
    }

    price_history {
        string id
        string item_id
        string variant_id
        int    price
        string starts_at
        string ends_at
    }

    stock_movements {
        string id
        string item_id
        string variant_id
        string location_id
        string sale_line_id
        string type
        float  quantity
        int    cost_price
        string created_at
    }

    %% Relationships

    tax_codes  ||--o{ items         : "tax_code"
    brands     ||--o{ items         : "brand"
    categories ||--o{ items         : "category"
    categories ||--o{ categories    : "parent_of"

    items         ||--o{ item_barcodes    : "has_barcode"
    items         ||--o{ item_images      : "has_image"
    items         ||--o{ item_variants    : "has_variant"
    item_variants ||--o{ variant_barcodes : "has_barcode"

    stock_locations ||--o{ inventory      : "stock_at"
    items           ||--o{ inventory      : "stock_for_item"
    item_variants   ||--o{ inventory      : "stock_for_variant"

    items         ||--o{ price_history    : "price_for_item"
    item_variants ||--o{ price_history    : "price_for_variant"

    stock_locations ||--o{ stock_movements : "moves_at"
    items           ||--o{ stock_movements : "item_movement"
    item_variants   ||--o{ stock_movements : "variant_movement"

```
---
# 3️⃣ Sales, Cash, Customers & Audit – Rich Detail
```mermaid
erDiagram

    customers {
        string id
        string name
        string phone
        string email
        string address
        string loyalty_no
        string created_at
    }

    users {
        string id
        string username
        string display_name
        string role
        string pin_hash
        boolean is_active
    }

    stock_locations {
        string id
        string name
    }

    registers {
        string id
        string name
        string location_id
        boolean is_active
    }

    payment_methods {
        string id
        string name
        string type
        boolean is_active
        int    sort_order
    }

    items {
        string id
        string name
    }

    item_variants {
        string id
        string item_id
        string name
    }

    sales {
        string id
        string receipt_no
        string status
        string sale_type
        string register_id
        string cashier_id
        string customer_id
        string currency
        int    subtotal
        int    discount_total
        int    tax_total
        int    total
        int    rounding
        string created_at
        string completed_at
        string voided_at
    }

    sale_lines {
        string id
        string sale_id
        int    line_no
        string item_id
        string variant_id
        string name_snapshot
        string sku_snapshot
        string barcode_snapshot
        float  quantity
        int    unit_price
        int    line_discount
        int    tax_rate_bp
        int    tax_amount
        int    total_before_tax
        int    total_after_tax
    }

    payments {
        string id
        string sale_id
        string method_id
        int    amount
        string currency
        string reference
        int    change_given
        string paid_at
    }

    sale_discounts {
        string id
        string sale_id
        string line_id
        string type
        int    value
        int    amount
        string reason
    }

    sale_links {
        string id
        string sale_id
        string original_sale_id
        string reason
    }

    shifts {
        string id
        string register_id
        string cashier_id
        string opened_at
        string closed_at
        int    opening_cash
        int    closing_cash
        int    expected_cash
        string note
    }

    stock_movements {
        string id
        string item_id
        string variant_id
        string location_id
        string sale_line_id
        string type
        float  quantity
        int    cost_price
        string created_at
    }

    audit_log {
        string id
        string actor_id
        string entity_type
        string entity_id
        string action
        string data_json
        string created_at
    }

    %% Relationships

    stock_locations ||--o{ registers : "has"
    registers       ||--o{ sales     : "records"
    users           ||--o{ sales     : "cashier"
    customers       ||--o{ sales     : "customer"

    sales     ||--o{ sale_lines      : "has_lines"
    sales     ||--o{ payments        : "has_payments"
    sales     ||--o{ sale_discounts  : "has_discounts"
    sales     ||--o{ sale_links      : "links"

    sale_lines ||--o{ sale_discounts : "line_discount"
    payment_methods ||--o{ payments  : "via"

    registers ||--o{ shifts          : "has_shifts"
    users     ||--o{ shifts          : "opens"

    items         ||--o{ sale_lines  : "line_item"
    item_variants ||--o{ sale_lines  : "line_variant"

    stock_locations ||--o{ stock_movements : "at"
    items           ||--o{ stock_movements : "item_movement"
    item_variants   ||--o{ stock_movements : "variant_movement"
    sale_lines      ||--o{ stock_movements : "from_line"

    users ||--o{ audit_log          : "actor"

```
---
# 4️⃣ Plugins & Shortcuts – Rich Detail
```mermaid
erDiagram

    plugin_catalog {
        string id
        string version
        string name
        string description
        string author
        string website
        string repository_url
        string runtime
        string entrypoint
        string package_url
        string sha256
        string signature
        int    size_bytes
        string min_pos_version
        string max_pos_version
        string api_version
        string tags_json
        string capabilities_json
        string published_at
        boolean is_deprecated
    }

    plugins {
        string id
        string name
        string version
        string install_state
        string description
        string author
        string website
        string entrypoint
        string runtime
        string installed_from_url
        string installed_sha256
        boolean is_active
        string trust_level
        string installed_at
        string updated_at
    }

    plugin_entries {
        string id
        string plugin_id
        string type
        string key
        string label
        string icon_path
        int    sort_order
        boolean is_active
        string parent_page_key
        string menu_group
        string route
        string target_action
        string trigger_event
        string config_json
        string created_at
        string updated_at
    }

    plugin_settings {
        string id
        string plugin_id
        string key
        string value_json
        string scope
        string scope_id
        string updated_at
    }

    plugin_hooks {
        string id
        string plugin_id
        string event
        string action
        int    priority
        boolean is_active
        string config_json
    }

    plugin_permissions {
        string id
        string plugin_id
        string permission
        boolean granted
    }

    items {
        string id
        string name
    }

    shortcut_buttons {
        string barcode
        string item_id
        string label
        string image_path
    }

    %% Relationships

    plugin_catalog ||--o{ plugins           : "version_of"
    plugins        ||--o{ plugin_entries    : "exposes"
    plugins        ||--o{ plugin_settings   : "configured_by"
    plugins        ||--o{ plugin_hooks      : "hooks"
    plugins        ||--o{ plugin_permissions: "requires"

    items ||--o{ shortcut_buttons          : "shortcut_for"

```

