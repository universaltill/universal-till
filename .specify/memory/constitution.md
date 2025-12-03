# UniversalTill POS Constitution
_Last updated: 2025-12-03_

---

## Purpose

UniversalTill is an open-source, plugin-based Point of Sale (POS) system for small and medium retailers worldwide.

It aims to:

- Be **local-first and resilient** (works during network outages).
- Provide a **clean, well-structured domain model** for catalog, inventory, sales, and plugins.
- Allow **third-party developers** to extend the system via a powerful plugin ecosystem.
- Run on **modest hardware** (mini PCs, Raspberry Pi-class devices) and support multiple UIs over time.

This constitution defines how UniversalTill is designed, evolved, and maintained across all features—from the MVP to long-term roadmap.

---

## Core Principles

### 1. Correctness Over Cleverness
- Favor straightforward, explicit solutions over clever tricks.
- Monetary values use **integer minor units** (e.g., pence).
- Data integrity (sales, stock, payments) outweighs micro-optimizations.

### 2. Stable Domain Model
- The core domains—**Catalog**, **Inventory**, **Sales**, and **Plugins**—change less frequently than transport/UI.
- Breaking changes to domain models must be deliberate and documented.
- Historical data must remain readable indefinitely.

### 3. Predictable Plugin Contracts
- Plugin APIs (payment, device, delivery, pricing, tax, etc.) are **versioned** and explicit.
- Plugins never read internal app or DB structures directly.
- Plugins must declare **capabilities** and **permissions** enforced by the host.

### 4. Local-First & Resilient
The POS must complete a full sale offline:

- browse/select items  
- compute totals/tax  
- accept payments (depending on device)  
- persist sales + stock movements  
- print a receipt  

Cloud sync is optional, not required.

### 5. Testable Core
- Domain logic in `core/` must be testable without DB, network, or UI.
- Side effects live in adapters.
- Each feature needs:
  - happy-path tests  
  - edge-case tests (money, rounding, stock)

### 6. Security & Data Integrity
- Validate all external input (user, plugins, devices, integrations).
- Critical state transitions are auditable.
- Sensitive data (API keys, PINs, secrets) must never be logged.

### 7. Backend-Led UI & Tests
- Implement or update backend flows, handlers, and contracts **before** building UI for them.
- Every UI change must ship with automated tests (Go handler/UI smoke tests or equivalent) covering the rendered controls and workflows.
- UI forms and controls must be wired to live backend data; no stubs or hardcoded payloads are permitted in production templates.

---

## Domains & Current Feature Set

This section captures what actually exists today so developers and AI agents operate on truthful system boundaries.

---

## Relational Data Model (SQLite)

UniversalTill uses SQLite as the local, authoritative data store.

- Migrations live in `internal/db/migrations/*.sql`
- The base schema is `001_init.sql`
- Applied migrations are recorded in `schema_migrations`

### Conventions
- Money = integer minor units.
- Foreign keys enabled: `PRAGMA foreign_keys = ON`.
- Soft-delete via `is_active` flags.
- Shared constraint:
```
CHECK (
(item_id IS NOT NULL AND variant_id IS NULL)
OR (item_id IS NULL AND variant_id IS NOT NULL)
)
```


### Core Tables (from 001_init.sql)

#### Migrations
- `schema_migrations(version, applied_at)`

#### Global Settings
- `settings(key PK, value, updated_at)`

#### Lookups & Catalog
- `tax_codes`
- `categories` (self-referencing)
- `brands`
- `stock_locations`

#### People / Tills / Payment Methods
- `customers`
- `registers`
- `users`
- `payment_methods`

#### Items, Variants, Barcodes, Images
- `items`
- `item_barcodes`
- `item_images`
- `item_variants`
- `variant_barcodes`

#### Inventory & Pricing
- `inventory`
- `price_history`

#### Sales System
- `sales`
- `sale_lines`
- `payments`
- `sale_discounts`
- `sale_links` (returns)
- `shifts`
- `audit_log`

#### Stock Movements
- `stock_movements` (every stock-affecting operation)

#### Plugin System
- `plugin_catalog`
- `plugins`
- `plugin_entries`
- `plugin_settings`
- `plugin_hooks`
- `plugin_permissions`

#### UI Shortcuts
- `shortcut_buttons`

> **Rule for all contributors (human or AI):**  
> Any modification to these tables requires:
> - a new migration under `internal/db/migrations/`
> - updates to `docs/data-model.md` and relevant ER diagrams  
> - preserving historical data semantics  

For full schema and diagrams, see `docs/data-model.md`.

---

## Catalog Domain

Core entities: Item, Variant, Category, Brand, TaxCode, Barcodes, Images, PriceHistory.

Behaviors:
- Items/variants may have multiple barcodes + images.
- Prices stored historically.
- Category hierarchy supported.
- Weighted items tracked with REAL units.
- Soft deletes instead of hard deletes.

---

## Inventory Domain

Entities: StockLocation, Inventory, StockMovement.

Behaviors:
- StockMovement is the source of truth.
- Inventory = aggregate of movements.
- Sales/returns automatically generate movements.
- Weighted items use REAL quantities.

---

## Sales Domain

Entities: Sale, SaleLine, Payment, PaymentMethod, Discounts, Links, Shifts, AuditLog.

Behaviors:
- Snapshot values recorded (names, SKUs, barcodes, tax rate, totals).
- Multiple payments per sale.
- Returns modeled as separate sales.
- Shifts track cash drawer sessions.
- Audit log records critical events.

---

## Plugin System

Entities: PluginCatalog, Plugin, PluginEntry, PluginSettings, PluginHooks, PluginPermissions.

Supported plugin types:
- `page`, `button`, `popup`,  
- `payment`, `device`, `hardware`,  
- `integration`, `delivery`,  
- `pricing`, `tax`,  
- `report`, `import`, `export`,  
- `background_job`, `scheduler`,  
- `customer_facing`, `receipt_template`,  
- `auth`, `notification`.

Behaviors:
- Plugins may add UI, logic, background tasks.
- Capabilities declared; permissions enforced.
- Configurable per-plugin via JSON.
- Plugins may subscribe to events (e.g., `sale.completed`).

---

## Hardware & Devices

Concepts:
- Device plugins (printers, scanners, scales, card readers).
- Hardware plugins (device hubs, display controllers).
- Standardized device contracts: print, open drawer, read weight, etc.

---

## Integrations

Example integration areas:
- Delivery platforms  
- Accounting systems  
- E-commerce  
- Reporting/exports  

Integrations usually subscribe to events and expose settings/UI via plugins.

---

## MVP Scope

1. Local Catalog Management  
2. Core Sales Flow  
3. Basic Inventory  
4. Basic Plugin Host  
5. Shifts & Cash Drawer  
6. Offline-First Operation  

---

## Near-Term Roadmap

1. Plugin Store Integration  
2. Delivery Plugins  
3. Advanced Pricing & Promotions  
4. Tax Plugins  
5. Reporting & Analytics  
6. Customer Accounts & Loyalty  
7. Cloud Sync & Remote Management  

---

## Architecture & Project Structure (Go)

Folders:

- `core/` – domain models, pure logic  
- `app/` – use cases / interactors  
- `adapters/` – SQLite, HTTP clients, devices  
- `plugins/` – plugin runtime + builtin plugins  
- `ui/` – POS UI  

Rules:
- `core/` has no external deps.
- `app/` defines interfaces.
- `adapters/` implement interfaces.
- Plugins depend on stable contracts.

---

## Migrations & Versioning

UniversalTill uses **SQLite** as its local data store.  
The database schema is treated as a **public API contract** and evolves only through
versioned SQL migrations.

### Migration Files
- All schema changes MUST be implemented as new migration files in  
`internal/db/migrations/`.
- Filenames follow the pattern:

```
00X_description.sql
```

- Migrations are **append-only**; existing migration files MUST NOT be edited.

### Applying Migrations
- Applied versions are tracked in `schema_migrations(version, applied_at)`.
- The application:
- Enables `PRAGMA foreign_keys = ON`.
- Detects pending migration files.
- Applies migrations in numeric order.

### Compatibility Rules
- Changes MUST respect:
- `001_init.sql`
- `docs/data-model.md`
- Backwards-incompatible changes are prohibited unless:
- explicitly requested in a spec, AND  
- accompanied by a full data-migration strategy.
- Forbidden without explicit approval:
- renaming tables/columns  
- dropping columns  
- changing types/semantics  
- reusing names for new meanings  

### Preferred Schema Evolution
- Add new tables.
- Add new nullable or defaulted columns.
- Add indexes or lookup tables.
- Preserve historical meanings of:
- `sales`
- `sale_lines`
- `stock_movements`
- `price_history`

### Documentation Requirements
Every schema change MUST update:

- the migration files  
- `docs/data-model.md`  
- the relevant Mermaid ER diagrams  

Large diagrams must be split into sections instead of removed.

### Offline Safety
Migrations must be:

- forward-only  
- idempotent  
- safe for devices upgrading late  

---

## Workflow with Spec Kit

1. **Constitution**  
 Managed in `.specify/memory/constitution.md`.

2. **Specify**  
 `/speckit.specify "Feature description"` creates/updates specs under `docs/features/`.

3. **Plan**  
 `/speckit.plan` generates an implementation plan respecting this constitution.

4. **Tasks**  
 `/speckit.tasks` generates small, testable tasks.

5. **Implement**  
 `/speckit.implement` writes code following tasks and boundaries.

Human review required for all AI-generated proposals.

---

## Governance

- The constitution supersedes ad-hoc practices.  
- Changes to core domains, plugin contracts, or persistence require:
- rationale  
- migration plan  
- documentation updates  
- testing  

All feature work begins with a **spec**.

### Contribution Workflow

- All changes must land via a feature branch and PR; avoid direct commits to `main` unless explicitly authorized.

---

**Version:** 1.1.0  
**Ratified:** 2025-11-26  
**Last Amended:** 2025-12-03
