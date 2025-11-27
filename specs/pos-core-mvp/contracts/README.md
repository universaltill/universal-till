# Contracts: Basic Plugin Host (POS Core MVP)

**Date**: 2025-11-27  
**Scope**: Use existing plugin tables; no new contracts added.

## Persistence Expectations

- `plugins`: manifest metadata (id/version/label/route/type/status).  
- `plugin_entries`: exposed UI entries (page/button/popup/customer_facing/receipt_template).  
- `plugin_settings`: JSON configs per plugin.  
- `plugin_permissions`: declared capabilities/permissions stored for enforcement.  
- `plugin_hooks`: subscribed events.

## Host Responsibilities

- Enforce declared permissions before exposing UI/actions.  
- Render entries in navigation/shell using stored metadata.  
- Avoid direct DB access by plugins; only contract-driven interactions.  
- Keep plugin state changes auditable (`audit_log`).

## Non-goals

- No new plugin types or schema changes.  
- No external plugin registry changes beyond existing flow.
