# Data Model Notes: SQL Access Refactor to Data Repos

**Feature**: specs/001-sql-repo-refactor/spec.md  
**Date**: 2025-12-09

This feature does not introduce new entities or schema changes. All tables and relationships remain as defined in `internal/db/migrations/001_init.sql` and `docs/data-model.md`.

## Repository Mapping

- **Catalog & Pricing**: Items, variants, barcodes, images, price_history → consolidate queries in a catalog-oriented repo (e.g., `catalog_repo.go`), preserving XOR item/variant constraints and active filters.
- **Inventory & Stock Movements**: inventory, stock_movements → repository methods handle aggregates and movement inserts within caller-managed transactions.
- **Sales & Payments**: sales, sale_lines, sale_discounts, payments, sale_links, shifts → repository methods manage read/write flows with existing rounding and snapshot semantics.
- **Plugins**: plugin_catalog, plugins, plugin_entries, plugin_settings, plugin_permissions, plugin_hooks → repository methods handle installs/updates/audit linkage without leaking schema to plugins.
- **Settings & Shortcuts**: settings, shortcut_buttons → centralize reads/writes to avoid ad-hoc queries.
- **Audit & Telemetry**: audit_log → repository methods record events consistently, maintaining sensitive-data redaction rules.

## Transaction Patterns

- Repository methods must accept caller-managed transaction/context where flows already wrap multiple operations; no expansion of transaction scope.  
- Prefer signatures that take a `context.Context` plus an explicit handle (`*sql.Tx` when available, falling back to the repo’s `*sql.DB`). Example: `func (r *POSRepo) SaveFoo(ctx context.Context, tx *sql.Tx, ...) error` should use `tx` when non-nil and never open a new transaction if one is provided.  
- Read-only methods should work with either a passed-in transaction or the base DB handle so callers can compose multiple reads inside their own transaction.  
- Multi-step operations (e.g., sale insert + movements + audit) should remain atomic when invoked inside a transaction; repositories should avoid opening nested transactions.  

## Validation & Constraints

- Preserve existing constraints (e.g., `(item_id XOR variant_id)` checks, foreign keys ON).  
- No schema changes; any future migration must follow constitution rules (not in scope here).  
- Error semantics must match pre-refactor behavior; repository methods wrap but do not alter classification.
