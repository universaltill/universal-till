# Code review — plugin payment-key hijack: ownership-guarded sync + install validation + repair

- **Date**: 2026-07-30
- **Branch**: `fix/plugin-payment-key-hijack`
- **Scope**: the batch-9-review finding that a plugin declaring
  `{type:"payment", key:"cash"}` captured the built-in cash tender and —
  once the plugin was disabled — the sync's deactivate step removed cash
  from checkout entirely. Brushes the offline-first non-negotiable
  ("checkout must never be blocked").
- **Decision record**: `ut-docs/adr/0031-plugin-payment-method-identity.md`
  — bare keys + ownership guard + install-time validation; namespacing
  (`<plugin_id>:<key>`) was rejected because `payments.method_id` prints
  verbatim on customer receipts and the reports payment breakdown, and a
  history remap of `payments.method_id` would ride along — traced in
  code before deciding, not assumed.
- **Independent review**: different-model (opus) subagent, findings below.

## What changed

1. **`SyncPluginPaymentMethods` ownership guard**: the upsert's
   `ON CONFLICT(id) DO UPDATE` now carries
   `WHERE payment_methods.plugin_id = excluded.plugin_id` and no longer
   reassigns `plugin_id`. A key colliding with a built-in
   (`plugin_id IS NULL` — the WHERE is NULL-safe by construction: `NULL =
   'x'` is never true), a shop-created tender, or another plugin's method
   leaves the existing row untouched. First owner wins.
2. **Install-time validation** (`plugins.PersistManifest`, step 0, inside
   the install transaction): payment entry keys must be non-empty,
   contain no `:` (reserved), not equal any non-plugin
   `payment_methods.id`, and not be owned by another plugin
   (`PluginRepo.FindPaymentKeyConflicts` checks both `payment_methods`
   and `plugin_entries`). The plugin's own keys never self-conflict —
   reinstall/upgrade pass. A rejected install rolls back whole.
3. **Migration `021_payment_method_hijack_repair.sql`**: any
   already-captured built-in (`cash`/`card`/`gift`) gets `plugin_id`
   cleared and `is_active = 1` restored. Names deliberately not reset
   (operators may rename tenders legitimately). Idempotent; only the
   three seeded ids are touched, so genuine plugin methods are never
   affected.

## Verification (pipeline side)

Seven tests written failing-first, each seen failing with the exact
capture it guards against:

- `plugin_repo_payment_guard_test.go` (real migrated DB, real seeds):
  hijack blocked both halves (capture AND the deactivate-after-disable
  that actually kills checkout); plugin's own lifecycle still works
  (materialize → deactivate on disable → reactivate on re-enable);
  cross-plugin collision leaves the first owner's row untouched;
  migration 021 re-executed against a hand-hijacked real DB repairs the
  built-in, is idempotent, and leaves genuine plugin methods alone.
- `payment_key_validation_test.go`: built-in collision rejected with the
  key named and no half-installed plugin left behind (tx rollback
  asserted); cross-plugin collision names the owning plugin; empty and
  `:` keys rejected; same-plugin upgrade with unchanged key passes.

`go build ./...`, `go vet ./...`, full `go test ./...`, both CI guards —
green.

## Independent (opus) review findings

The reviewer revert-probed all three original production pieces (each
revert failed exactly its matching tests), verified the SQL NULL
semantics (a false `DO UPDATE ... WHERE` is a skip, not an error),
confirmed no other `Sync*` function materializes `plugin_entries` into
another table, confirmed no ecosystem plugin declares `cash`/`card`/
`gift` (demo/qrpay/stripe/sumup all use unique keys), and confirmed
migration 021 composes with the tx-wrapped migration runner.

**BLOCKER it found — fixed (failing-first, then green):** LAN admin sync
re-hijacks a repaired replica. `payment_methods` is an admin-synced
table and `upsertRow` copied every column — including `plugin_id` and
`is_active` — so a not-yet-upgraded primary re-imported the captured
state onto a replica that had already run migration 021, and with no
hijacking plugin installed locally the deactivate step pinned cash
inactive on every sync. Two-part fix: `plugin_id` is now a `skipCols`
column for `payment_methods` (dropped from the dump AND ignored on
apply), and `SyncPluginPaymentMethods` re-asserts the built-in invariant
(never plugin-owned, always active) on every run. `is_active` still
syncs — a shop deliberately disabling a tender on the primary must
propagate; the transient damaged-primary window heals when the primary
upgrades and its own repaired state propagates.

**Should-fixes, all fixed (each failing-first):**
- Migration 021 left the hijacker's ENTRY live, and both payment
  dispatch gates match on entry key alone — a legacy squatter still
  received `payment.cash.requested` and could decline the authorize leg.
  021 now deactivates payment entries squatting on built-in keys.
- The deactivate step was ownership-blind (`id NOT IN (keys)`) — an
  unrelated plugin's same-key entry kept another plugin's tender alive
  after its owner was disabled. Now `NOT EXISTS` matching key AND owner.
- `Rollback` wrote payment entries with no validation (the one writer
  besides `PersistManifest`) — a legacy on-disk manifest could restore a
  colliding key. Validation hoisted into a shared
  `validatePaymentEntryKeys`, called by both; a failed rollback leaves
  current entries intact.
- A key reserved by an uninstalled plugin produced an error naming a
  plugin that isn't there. The reservation deliberately stands (the
  tender row anchors sales history; adopting it would re-attribute that
  history) but the error now says the owner is no longer installed.
- Keys with surrounding whitespace were accepted (`" cash"` would mint a
  padded id); now rejected.
- Test hardening from its false-pass hunt: name-survives-repair asserted
  (the ADR's explicit promise was untested), row-never-deleted asserted
  in the lifecycle test, the `payment_methods`-owner branch of
  `FindPaymentKeyConflicts` now exercised, message-content asserts on
  the format rejections.

**Accepted as-is / queued:**
- Shop-created tenders captured pre-fix aren't auto-repaired (can't be
  distinguished from genuine plugin methods) — queued: startup warning
  for plugin-owned methods whose plugin is absent.
- Orphan `plugin_catalog` row on a rejected marketplace install
  (pre-existing upsert-before-validate ordering) — queued as a nit.
- `payment_methods.name` UNIQUE can still hard-fail sync on a label
  collision — pre-existing, noted in the ADR.

## Final gate (after review fixes)

13 tests across `internal/data` + `internal/plugins` (7 original + 6
finding-regressions), every one seen failing first. `go build`,
`go vet`, full `go test ./...`, both CI guards — green.
