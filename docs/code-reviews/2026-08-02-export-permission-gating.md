# Code review: gate export.requested.ask's sales/stock ledgers on their own permissions

**Date:** 2026-08-02
**Scope:** `internal/pages/data_api.go`, `internal/pages/export_dispatch_test.go`,
`internal/plugins/permissions.go`, `internal/plugins/permissions_test.go`.
Companion changes: `ut-plugin-tax-de/manifest.json` (own repo/PR),
`ut-docs/reference/plugin-manifest.md` (own repo/PR).
**Trigger:** ut-docs#228 — found during independent review of ut-docs#221
(the `export.requested.ask` payload gaining a real `sales` ledger) and later
widened when ut-docs#59 added a second `stock` ledger to the same payload,
both still gated only by the generic `events:receive` permission.

## What shipped

`POST /api/data/export` resolves an installed export/report entry and asks
its owning plugin `export.requested.ask` with a payload carrying `sales`
(`data.ExportSaleRow`) and `stock` (`data.ExportStockRow`). Before this
change, the only gate was `events:receive` — checked deep inside
`EventBus.AskPlugin` — so any installed export plugin got the **full**
sales and stock ledgers regardless of what it actually declared needing.

- Each ledger is now gated independently: `sales` requires `sales:read`,
  `stock` requires `inventory:read`, both checked via
  `plugins.CheckPermissionGranted` (new — see below) against the resolved
  entry's owning plugin.
- Missing one or both never fails the request — the dispatcher still calls
  the plugin (still subject to the existing `events:receive` gate); the
  ungranted field(s) are simply `null`. A plugin needing neither ledger at
  all (e.g. `ut-plugin-tax-de`'s `dsfinvk-export-de` entry, purely
  fiskaly-triggered) still works.
- Companion change in `ut-plugin-tax-de`: added `sales:read` to its
  manifest (its DATEV Buchungsstapel entry consumes `Sales`; its DSFinV-K
  entry doesn't touch either ledger, so `inventory:read` was deliberately
  **not** added), version bumped 0.3.0 → 0.3.1.
- `ut-docs/reference/plugin-manifest.md`'s `export.requested.ask` contract
  section documents both new permissions.

## New/changed tests

- `TestExportDispatch_OmitsSalesWithoutSalesReadPermission`,
  `TestExportDispatch_OmitsStockWithoutInventoryReadPermission`,
  `TestExportDispatch_OmitsBothWithoutEitherPermission` (added post-review,
  see below) — `internal/pages/export_dispatch_test.go`.
- `seedExportPlugin` now grants `sales:read`/`inventory:read` by default
  (most tests want the full happy path); `seedExportPluginWithPermissions`
  added for the gating tests specifically.
- `TestCheckPermissionGranted_DistinguishesErrorFromDenial` (added
  post-review) — `internal/plugins/permissions_test.go`.

## Verification (self, before independent review)

- TDD: wrote the two gating tests first, confirmed both failed against the
  unmodified handler with real assertion output (`got [{"receipt_no":
  "R900",...}]` where `null` was expected), then implemented the fix,
  confirmed both pass.
- `go build ./... && go vet ./...`: clean.
- `go test ./...`: green except `TestSaveCleansUpDirectoryOnWriteFailure`
  (`internal/issuereport`) — pre-existing, already-filed ut-docs#258
  (fails under a root-run sandbox), unrelated to this diff.
- `bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-i18n.sh`:
  both green (no new SQL outside `internal/data`, no new user-facing
  strings).
- Beyond unit/HTTP-level tests: built the actual `ut-plugin-tax-de` WASM
  module and drove it through the real wazero runtime (`bus.AskPlugin`,
  real accounting settings seeded) with its new manifest permission
  granted — the real plugin built a genuine DATEV export file (2929 bytes)
  from live sales data. Confirms the companion manifest change actually
  prevents the regression it exists to prevent, not just that the unit
  tests for `datev.Build` still pass in isolation.

## Independent review

Different-model subagent (Opus), fully independent re-verification —
rebuilt/re-tested both repos from scratch, then specifically re-verified
the TDD claim itself: reverted the permission-gating logic back to the
unconditional pre-fix calls, re-ran the two gating tests, confirmed they
failed with the exact assertion output claimed above, restored the fix
(from a backup, not `git checkout --`, since the fix was uncommitted),
confirmed green again, and confirmed the working tree was byte-identical
to how it was handed off.

Also audited the leak surface directly rather than trusting the design
note: confirmed the *request* payload (as opposed to a plugin's response)
is never logged anywhere in `wasm_runtime.go`'s ask-result logging or the
audit-dispatch calls; confirmed `SalesForExport`/`StockForExport` have no
other caller; confirmed `AskPlugin`'s `sub.PluginID != pluginID` check
means the gate can't be checked against the wrong plugin; confirmed no
other host function exposes sales/inventory data to a plugin. Confirmed
neither of this pipeline's two recurring bug classes (missing
`os.MkdirAll`, cwd-relative path vs. `paths.Data`) applies — this diff
writes no files. No real client/shop names, no secret-shaped literals.

**Findings (all fixed):**

1. **Should-fix — doc/behavior mismatch, `plugin-manifest.md`.** The first
   draft claimed a plugin author "can't distinguish 'no data' from 'no
   permission' from the payload alone" for *either* ledger — true for
   `sales` (both cases are `null`/absent), false for `stock` (an ungranted
   `stock` is `null`, but a genuinely empty inventory is still `[]` per
   `StockForExport`'s own `make([]ExportStockRow, len(levels))`, confirmed
   never `nil`). **Fixed**: the doc now states this distinction explicitly
   per-field instead of as one blanket claim.

2. **Should-fix — a genuine DB error was silently treated as a permission
   denial.** The original code used `plugins.CheckPermission(...) == nil`
   as the gate. `CheckPermission` collapses three cases (granted, not
   granted, and a real infrastructure error) into a single error return —
   so a transient DB fault (e.g. `SQLITE_BUSY` past the busy-timeout on a
   till mid-export) would have been indistinguishable from "not granted"
   and silently shipped `sales: null`/`stock: null` as a `200`, rather than
   the pre-existing `500` behavior a DB fault on this path used to produce.
   A fiscal/accounting export plugin that doesn't itself reject empty
   input could write a valid-looking but empty file an operator files as
   their real month-end return. **Fixed**: added
   `plugins.CheckPermissionGranted(ctx, db, pluginID, permission) (bool,
   error)` — same semantics and same audit-on-denial side effect as
   `CheckPermission`, but returns a genuine infrastructure error separately
   from a legitimate not-declared/not-granted denial (`granted=false,
   err=nil`) instead of collapsing both into one error. `data_api.go` now
   500s on a real error and only omits the field on a legitimate denial.
   New test (`TestCheckPermissionGranted_DistinguishesErrorFromDenial`)
   closes a real DB connection and confirms the error surfaces rather than
   being folded into `granted=false`.

3. **Should-fix — doc accuracy, `plugin-manifest.md`.** Two problems in one
   sentence: (a) referred to "this repo's own `dsfinvk-export-de` entry" —
   that entry lives in `ut-plugin-tax-de`, not `ut-docs`; (b) implied
   per-*entry* permission scoping when the gate actually keys on
   `entry.PluginID` — a multi-entry plugin gets the **union** of whatever
   any of its own entries need (confirmed: `ut-plugin-tax-de` declaring
   `sales:read` for its DATEV entry means its DSFinV-K entry — the doc's
   own cited example of "needs neither ledger" — still receives the full
   sales ledger when triggered, since both entries share one plugin's
   permission set). This is a defensible, documented granularity choice
   (per-entry gating would need a real design change, not a doc fix), but
   the doc previously implied the exposure didn't exist. **Fixed**: the
   doc now states the per-plugin (not per-entry) granularity explicitly.

4. **Nice-to-have — no test for the neither-permission-granted case.**
   The design's own justification ("never fail, just omit — a plugin might
   need neither ledger") had no test proving it. **Fixed**: added
   `TestExportDispatch_OmitsBothWithoutEitherPermission`, modeled directly
   on the real `dsfinvk-export-de` scenario (a `fiskaly` entry key,
   `events:receive` only).

5. **Nice-to-have — dead field in a test.** `OmitsSalesWithoutSalesReadPermission`
   declared a `Stock` field in its payload struct and never asserted on
   it, so the test didn't actually prove the granted side of the gate
   still works (only that the ungranted side is omitted). **Fixed**: now
   asserts `len(payload.Stock) > 0` (that plugin has `inventory:read`
   granted), closing the gap the test's own comment claimed to cover.

**Accepted and deferred (not fixed, documented here instead):**

6. **Audit-log noise for an expected condition.** `CheckPermissionGranted`
   (like `CheckPermission`) writes a `permission_denied` audit row every
   time a plugin lacking a data permission triggers an export — including
   the entirely expected case of a plugin that legitimately needs neither
   ledger (e.g. `dsfinvk-export-de`, on every single trigger). Elsewhere in
   this codebase (`ipc.go`'s subscriber-permission checks) a denial
   genuinely does mean "something is misconfigured," so this makes real
   denials proportionally harder to spot in the audit log over time. Not
   fixed here: doing so cleanly would mean either a second, non-auditing
   permission-check helper (duplicating `CheckPermission`'s logic a third
   time) or teaching the audit log the difference between "expected,
   voluntary non-use" and "actual misconfiguration," neither of which is a
   one-line fix and both are a genuine design question beyond this card's
   scope. Left as-is; worth a Backlog card if audit-log signal-to-noise on
   this path becomes a real problem in practice.

## Verdict

**Safe to merge.** Independent review found three real, cheap doc/logic
issues (one of them a genuine correctness gap — DB errors silently
mis-treated as denials) plus two test-quality nits; all fixed and
re-verified (full suite + both guards green, new regression test for the
error-vs-denial distinction). One item accepted and explicitly deferred
rather than silently left unaddressed.
