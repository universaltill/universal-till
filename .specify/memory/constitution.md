# UniversalTill POS Constitution

_Last updated: 2025-11-26_

## Purpose

UniversalTill is an open-source, plugin-based Point of Sale (POS) system for small and medium retailers worldwide.

It aims to:

- Be **local-first and resilient** (works during network outages).
- Provide a **clean, well-structured domain model** for catalog, inventory, sales, and plugins.
- Allow **third-party developers** to extend the system via a powerful plugin ecosystem.
- Run on **modest hardware** (mini PCs, Raspberry Pi-class devices) and support multiple UIs over time.

This constitution defines how we design, evolve, and maintain UniversalTill across all features, from the current MVP to future roadmap items.

---

## Core Principles

### 1. Correctness Over Cleverness

- Favor straightforward, explicit solutions over clever tricks.
- Monetary values are stored as **integer minor units** (e.g. pence) in the core domain and database.
- Data integrity (especially sales, stock, and payments) is more important than micro-optimizations.

### 2. Stable Domain Model

- The core domains—**Catalog**, **Inventory**, **Sales**, and **Plugins**—are more stable than UI or transport.
- Breaking changes to domain models (tables, core structs, key workflows) must be deliberate, justified, and documented.
- Historical data (past sales, stock movements, plugin events) must remain readable after schema or code changes.

### 3. Predictable Plugin Contracts

- Plugin APIs (payment, device, delivery, pricing, tax, report, integration, etc.) are **versioned** and described as explicit contracts.
- Plugins communicate with the host only through these contracts—not via reading internal data structures directly.
- A plugin must declare its **capabilities** and required **permissions**; the host enforces these at runtime.

### 4. Local-First & Resilient

- The POS can complete a full sale flow entirely offline:
  - browse/select items
  - compute totals and tax
  - accept payments (when possible)
  - persist sales and stock movements
  - print a receipt
- Cloud sync, remote management, and online integrations are **layers on top**, not prerequisites.

### 5. Testable Core

- Domain logic in the **core** package(s) should be testable in isolation, without a database, network, or UI.
- Side effects (DB, HTTP, devices, OS calls) live behind **interfaces** in **adapters**.
- Every non-trivial feature has at least:
  - happy-path tests for primary flows
  - edge-case tests for money, rounding, and stock updates

### 6. Security & Data Integrity

- All external inputs (user input, plugins, devices, integrations) are validated.
- Critical state transitions (sales, refunds, stock movements, plugin installs, configuration changes) are auditable.
- Sensitive data (API keys, passwords, secrets) is stored securely and never logged in plaintext.

---

## Domains & Current Feature Set

This section captures **what we have or are actively shaping** today, so Spec Kit and AI agents can reason about the real system.

### Catalog

Core entities:

- Item, ItemVariant
- Category, Brand, TaxCode
- ItemBarcode, VariantBarcode
- ItemImage (thumb + main)
- PriceHistory

Key behaviors:

- Items and variants can have multiple barcodes and images.
- Prices are stored as integers (minor units) with history; changes do not retroactively alter old sales.
- Categories support hierarchy (parent/child).
- Items can be weighed or non-weighed (unit vs. kg).
- Soft-delete or inactive flags preserve history while removing items from normal UI.

### Inventory

Core entities:

- StockLocation
- Inventory (on-hand quantity per item/variant per location)
- StockMovement (every inventory-affecting event)

Key behaviors:

- Sales, returns, adjustments, receiving, and transfers all create **StockMovements**.
- On-hand inventory is the aggregate of movements per item/variant/location.
- Weighted items (kg) use REAL quantities; packaged items use integer counts where possible.

### Sales

Core entities:

- Sale (header)
- SaleLine
- Payment
- PaymentMethod
- SaleDiscount
- SaleLink (for returns referencing original sale)
- Shift (cash drawer sessions)
- AuditLog (for important events)

Key behaviors:

- A completed sale has snapshot fields:
  - subtotal, discount_total, tax_total, total, rounding.
- SaleLines capture **snapshot copies** of item name, SKU, barcode, tax rate, and prices at time of sale.
- Multiple payments per sale are supported (cash, card, vouchers, etc.).
- Returns are modeled as separate Sales with `sale_type='return'` and negative quantities (or equivalent).
- Every completed SaleLine that reduces stock must have a corresponding StockMovement.
- Shifts capture opening/closing cash and expected vs. counted amounts.

### Plugin System

Core entities (conceptual):

- Plugin Catalog entry (id, version, metadata, capabilities, compatibility)
- Installed Plugin (local install state)
- PluginEntry (page, button, popup, payment, device, integration, delivery, report, pricing, tax, import, export, hardware, background_job, scheduler, receipt_template, customer_facing, auth, notification)
- PluginSettings (per plugin, with optional scope: global, register, user)
- PluginHooks (event subscriptions, e.g. `sale.completed`, `inventory.low`, `shift.opened`)
- PluginPermissions (declared capabilities like `payments:charge`, `devices:usb`, `sales:read`)
- PluginDependencies (plugin-to-plugin dependencies)
- PluginMigrations (per-plugin DB migrations)

Key behaviors:

- Plugins can contribute **UI** (pages, buttons, popups, customer-facing views, receipt templates).
- Plugins can contribute **logic** (payments, delivery, pricing, tax, notification, auth, integrations).
- Plugins can contribute **background jobs** and schedulers for sync/maintenance.
- Permissions and capabilities are enforced by the host; untrusted plugins are restricted.

### Hardware & Devices

Key concepts:

- Device plugins for specific peripherals (printers, scanners, scales, cash drawers, card terminals, customer displays).
- Hardware plugins for higher-level subsystems (device hubs, Bluetooth/USB managers, kitchen screens, etc.).
- Device/hardware contracts define standardized operations (e.g. printReceipt, openDrawer, readWeight, showCustomerText) and error semantics.

### Integrations

Key integration areas:

- Delivery platforms (e.g. Uber Eats / Deliveroo / JustEat-style flows).
- Accounting systems (e.g. Xero, QuickBooks).
- E-commerce (web shop stock & pricing sync).
- Reporting / analytics exports.

Integrations are usually implemented as plugins that:
- subscribe to events (e.g. sale completed, item updated),
- expose configuration via PluginSettings,
- may add UI pages/buttons for management.

---

## MVP Scope

The **MVP** for UniversalTill POS includes:

1. **Local Catalog Management**
   - CRUD for items, categories, brands, tax codes.
   - Barcodes and images for items and variants.
   - Basic price management with history.

2. **Core Sales Flow**
   - Build a cart via item selection or barcode scanning.
   - Apply basic discounts (fixed and percentage).
   - Support cash and card payments (at least one card method, possibly via a simple payment plugin or mock).
   - Persist sales in SQLite with correct snapshot totals and tax.
   - Update inventory via StockMovements.
   - Generate a structured receipt model for printing.

3. **Inventory Basics**
   - On-hand inventory per location.
   - Automatic decrement on sale and increment on return.
   - Manual adjustments for corrections.

4. **Basic Plugin Host**
   - Ability to install/enable/disable simple plugins.
   - Support at least:
     - a page plugin (adds a new screen),
     - a button plugin (adds a button on an existing page),
     - a simple payment or device plugin.
   - Permissions and settings primitives in place.

5. **Shifts & Cash Drawer**
   - Open/close shifts with opening/closing cash.
   - Associate sales with shifts.
   - Simple shift reconciliation report.

6. **Offline-First Behavior**
   - All of the above work without network connectivity.
   - No hard dependency on cloud for core POS.

---

## Near-Term Feature Roadmap

Beyond MVP, we recognize the following as high-priority:

1. **Plugin Store Integration**
   - Sync plugin catalog from a central store.
   - Install/update plugins from catalog entries with integrity verification (e.g. sha256).
   - Allow per-register plugin configuration.

2. **Delivery Plugins**
   - Unified contract for delivery providers (incoming orders, status updates, payouts).
   - At least one reference integration (e.g. “Mock Delivery Provider”) using this contract.

3. **Advanced Pricing & Promotions**
   - Pricing rules and discount plugins (BOGO, mix & match, time-based offers).
   - Clear precedence and composition rules.

4. **Tax Plugins**
   - Allow different tax strategies (VAT vs. sales tax, eco taxes).
   - Localized tax computation plug points.

5. **Reporting & Analytics**
   - Core reports: sales by day, by cashier, by item/category, tax breakdown, stock on hand.
   - Plugin-based reports for custom analytics.

6. **Customer Accounts & Loyalty**
   - Customers with optional loyalty accounts.
   - Hooks for loyalty plugins to award/redeem points.

7. **Cloud Sync & Remote Management (Phase 2)**
   - Sync selected entities (items, prices, sales summaries) to a central service.
   - Remote config for registers and plugins.
   - Multi-store support.

This roadmap should be refined as feature specs are created via `/speckit.specify`.

---

## Architecture & Project Structure

Target high-level structure (Go-based):

- `core/` – domain models, invariants, and pure business logic for:
  - catalog, inventory, sales, plugins, devices, integrations
- `app/` – use cases and orchestrators (e.g. CreateSale, ApplyDiscount, InstallPlugin)
- `adapters/` – SQLite persistence, HTTP clients, device drivers, OS integration
- `plugins/` – plugin host runtime, contracts, built-in plugins
- `ui/` – POS UI (desktop/web/mobile) and view models

Rules:

- `core/` must not depend on `adapters/` or `ui/`.
- `app/` may depend on `core/` and define interfaces that `adapters/` implement.
- Plugins depend on stable contracts, not on internal app structures.

---

## Workflow with Spec Kit

We use Spec Kit (Codex + sh variant) to guide changes:

1. **Constitution**  
   - `/speckit.constitution` updates this document: `.specify/memory/constitution.md`.

2. **Specify**  
   - `/speckit.specify "Short feature description"`  
   - Produces or updates a feature spec file (ideally under `docs/features/` in the POS repo).
   - Feature specs must define MVP slices (independently testable user stories).

3. **Plan**  
   - `/speckit.plan` creates an implementation plan constrained by:
     - this constitution
     - the existing codebase
     - the DB schema and plugin contracts

4. **Tasks**  
   - `/speckit.tasks` turns the plan into small tasks aligned with user stories and code structure.

5. **Implement**  
   - `/speckit.implement` produces code changes following the tasks and respecting boundaries.

All AI-generated proposals must be reviewed by a human before merging.

---

## Governance

- This constitution supersedes ad-hoc practices. If something in the codebase conflicts with it, we either:
  - update the code to comply, or
  - formally amend this document via `/speckit.constitution` and a documented decision.
- Changes to core domains, plugin contracts, or persistence must:
  - include an explanation in a spec/plan or short decision record,
  - be tested where practical,
  - consider migration of existing data.
- Feature work should always start from a **spec**, even if minimal.

**Version**: 1.0.0 | **Ratified**: 2025-11-26 | **Last Amended**: 2025-11-26
