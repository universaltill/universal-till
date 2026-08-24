# 2026-08-24 — §146a Abs. 4 AO fiscal register (data capture), ut-docs#665

## What shipped

A manager-visible register of German tills and their TSE (fiscal signing
device) fields, required by §146a Abs. 4 AO's till-notification duty,
grouped by business location:

- Migration `059_fiscal_register_de.sql` — `stock_locations` gains
  `address_street`/`address_postcode`/`address_city`; new
  `fiscal_register_de` table (one row per till/TSE pairing; decommission
  stamps a date, never deletes the row).
- `internal/pages/fiscal_register_page.go` — `GET /fiscal-register`, plus
  `POST /api/fiscal-register` (create), `POST /api/fiscal-register/{id}/decommission`,
  `POST /api/fiscal-register/locations/{id}/address`. Structural mirror of
  `registers_page.go`/`locations_page.go`: gated on the existing
  `"settings"` permission action, audited via `InsertAudit`.
- `internal/data/fiscal_repo.go` — `CreateFiscalRegisterDE`,
  `ListFiscalRegisterDE` (LEFT JOIN registers→stock_locations, so an
  unassigned register still lists), `DecommissionFiscalRegisterDE`,
  `SetStockLocationAddressDE`.
- `web/ui/pages/fiscal_register.html` + help topics in `en`/`ar`/`fa`/`tr`
  (`web/help/*/fiscal-register.md`), reusing existing `page-head`/`card`/
  `users-layout`/`table`/`pos-notice`/`empty` CSS classes.
- i18n: `fiscalregister.*` keys added to all four locales.

**Explicitly out of scope** (split into ut-docs#937 during Architect-phase
scoping): any export — human-readable or ELSTER XML — and filing on the
shop's behalf. Also out of scope: auto-filling fields from TSE
provisioning (depends on ut-docs#801/#802, still in progress elsewhere) —
every field here is manually entered.

**Placement** (core, not `ut-plugin-tax-de`) is a deliberate architecture
call, not an oversight of ADR-0050's table: the plugin engine has no host
function to enumerate its own stored records (`storage_get`/`storage_set`
only) and no mechanism to render a dynamic manager-facing page (`type:"page"`
manifest entries serve only static pre-shipped content). A growing,
manager-editable register can't be plugin-owned with today's primitives —
recorded on the issue so the boundary doesn't need re-deriving next time.

## Independent review

Spawned as an `Agent` (`general-purpose`, model `opus` — this card is
`complexity:medium`, built at Sonnet) in an isolated git worktree, per the
`reviewer` skill (worktree isolation used specifically so its TDD
revert-then-restore probes never touched the shared orchestrator checkout).

**Verdict: 1 blocker, 3 should-fix, 3 nits — all addressed below.**

| # | Finding | Fix |
|---|---|---|
| B1 | `guard-docs-shots.sh` failed — new routed page had no screenshot in any locale | Ran `make docs-shots` (reused the pre-installed Chromium at `/opt/pw-browsers` via `resolve-chromium.sh`, ut-docs#622) and committed all 4 new screenshots + the refreshed manifest |
| S1 | Permission-gate test only covered `GET /fiscal-register`; all 3 mutating POST routes were untested (production code *was* correctly gated — this was a coverage gap, proven live by the reviewer's TDD probe: bypassing the gate on all 3 POST handlers left every test in the package passing) | Extended `TestFiscalRegisterPage_ManagerGate` to assert 403 for a cashier on create/decommission/address-update, and that the blocked calls had no effect on the DB |
| S2 | Help topic's steps 2/3 were in an order the page doesn't allow (address form only renders once a location has ≥1 entry, but the topic said "set the address" before "add an entry") | Reordered steps in all 4 locale help topics; noted the precondition explicitly |
| S3 | The one-month due banner only ever covered acquisition, though decommissioning carries its own §146a notification duty (the AO form's own `Außerbetriebnahme-Datum` field) | Added a second `DecommissionDueSoon` flag/banner (`fiscalregister.banner.due_decommission`, all 4 locales), and updated the help topic's "Good to know" section to describe both triggers |
| N1 | Two dead locale keys (`fiscalregister.col.location`, `fiscalregister.address.title`) added to all 4 files but never referenced | Removed from all 4 locale files (confirmed zero references first) |
| N2 | `.fiscal-register-group` had no CSS rule — consecutive location blocks ran into each other with no visual separation | Added `margin-block-end` spacing in `web/public/app.css`, matching this file's existing logical-property convention |
| N3 | The "add entry" till picker used `ListRegistersForAdmin` (includes deactivated registers, unmarked) instead of the active-only list `registers_page.go`'s own create-form picker uses | Switched to `ListRegisters` (active-only) |

### TDD re-verification the reviewer ran (both reverted afterward)

- **Gate probe**: replaced the `canPerform` check with `if false` →
  `TestFiscalRegisterPage_ManagerGate` failed with a real assertion
  (`cashier GET /fiscal-register = 200, want 403`), not a compile error.
  Then, narrower: bypassed `requireManager` on only the 3 POST handlers
  with the GET gate left intact — **the entire package still passed**,
  confirming S1 was a real coverage gap (now closed above).
- **Server-stamping probe**: changed the decommission handler to read
  `decommissioned_on` from the POST body instead of `time.Now()` →
  the existing `TestFiscalRegisterPageDecommission` failed
  (`decommissioned_on = "1999-01-01", want "2026-08-24"`), confirming
  that assertion is genuine, not tautological.

### Also verified independently (Tester step, before review)

- Full `go test ./...` clean.
- `go test ./internal/data/... ./internal/pages/... ./internal/db/... -race`
  clean (this environment's `internal/pages`/`internal/data` packages are
  known-slow-but-clean under `-race`, needing an explicit `-timeout 15m`+
  — the default 10-minute per-package timeout is a known false "FAIL" here,
  not a hang; see this repo's other 2026-08-24 review records for the same
  note).
- A real running instance (fresh temp SQLite, `UT_AUTH=off`) driven end to
  end over real HTTP: created a location + register, added a fiscal
  register entry, set its location's address, confirmed the one-month-due
  banner rendered for a recently-acquired entry, decommissioned it,
  confirmed the row stayed listed with status flipped and the date
  server-stamped to today.
- Real browser screenshots (Playwright against the pre-installed
  Chromium): light theme, and `fa` (RTL) — nav mirrored, labels
  right-aligned above their fields, table columns flow correctly, no
  overlap/clipping. Dark theme could not be genuinely exercised in this
  sandbox (`themes/dark.css` 404s with a MIME-type error under the ad-hoc
  temp-dir binary) — reproduced identically on the pre-existing
  `/registers` page, confirming it's an environment artifact of this
  build, not a regression in this diff.
- Empty-state screenshot reviewed after the N-fix round (no entries yet)
  in both `en` and `fa` — confirmed the new `fiscalregister.empty` message
  renders correctly in both directions.

## Guards (all CI-blocking guards in `.github/workflows/ci.yml`'s `build` job)

`guard-data-access`, `guard-kiosk-engine`, `guard-plugin-menu-read`,
`guard-i18n`, `guard-compliance-claims`, `guard-docs-shots`,
`guard-help-topics`, `guard-webkit-version`, `guard-kiosk-launch-flags`,
`guard-android-status-address`, `guard-android-i18n`, `guard-emoji-font`,
`guard-htmx-loaded`, `guard-autofill-suppression`, `check-brand-assets`,
`guard-makefile-version` — all pass.

## Safe-to-merge verdict

Yes. No secrets or real shop/client names in the diff (test data is
generic: "AwesomePOS", "Front Till", "Hauptstraße 1, Berlin"). No SQL
outside `internal/data`/`internal/db`. Merge method: `merge` (never
squash/rebase), per this repo's standing attribution rule.

## Explicitly deferred

- Export (human-readable summary, then ELSTER XML) — **ut-docs#937**,
  depends on this card, in Backlog.
- Auto-filling fields from TSE provisioning — depends on ut-docs#801/#802.
- A dedicated `stock_location_management`/`register_management` permission
  action instead of reusing `"settings"` — tracked separately on ut-docs#903.
