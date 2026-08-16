# Code review — sale.completed payments:reconciliation permission (ut-docs#791)

- **Date:** 2026-08-16
- **Branch:** `feat/791-sale-completed-payments-reconciliation-permission`
- **Card:** universaltill/ut-docs#791 (`complexity:medium`, `security`)
- **Author model:** Sonnet (inline) · **Independent review:** Opus
  (fresh-context subagent, isolated worktree) · per the pipeline's model
  routing.

## What shipped

`sale.completed`'s card-present reconciliation fields (masked PAN, auth
code, terminal/trace ID — ut-docs#543) were delivered to **every** plugin
subscribed to `sale.completed`, with no distinction between "just wants
the sale for ERP sync" and "actually needs card reconciliation data."

Decision (this card's own text: an engineering/security-architecture call,
no ADR needed, no business decision needed): gate those four fields
behind a new plugin permission, `payments:reconciliation`, same shape as
the existing `sales:read`/`inventory:read` export-ledger gate
(ut-docs#228, `internal/pages/data_api.go`). A subscriber missing the
grant still receives the event — line items, totals, the non-card
payment method/amount/reference are unaffected — just with those four
fields absent (`omitempty`, genuinely missing from the JSON, not
empty-string) rather than being denied the whole subscription.

- `internal/plugins/ipc.go`, `publish()`: for `eventType ==
  "sale.completed"`, a redacted payload variant is built once (via
  `redactCardPresentFields`) by round-tripping the already-marshaled
  `payloadBytes` back through `json.Unmarshal` into a `SaleCompletedEvent`
  — not a type assertion against the original `payload interface{}` (see
  MAJOR 1 below for why). Inside the per-subscriber dispatch loop, each
  subscriber gets either the full or the redacted bytes depending on
  `data.NewPluginRepo(db).CheckPermission(ctx, sub.PluginID,
  "payments:reconciliation")` — the same DB-only primitive
  `CheckPermissionGranted` wraps, called directly to skip its
  audit-on-denial (see MAJOR 2).
- `internal/plugins/sale_event_reconciliation_permission_test.go` (new):
  6 tests — redaction without the permission (asserting the JSON keys are
  genuinely absent via `omitempty`, not just empty-string), full payload
  with the permission granted, a permission *declared but not granted*
  still redacts, a non-`sale.completed` event is unaffected, two
  subscribers (one granted, one not) in the same dispatch — no aliasing,
  and 5 sales to a permanently-ungranted subscriber write zero
  `payments:reconciliation` audit-denial rows.
- `ut-docs/architecture/plugin-architecture.md` (sibling repo): one
  paragraph under "Events & permissions" documenting the gate.

No new ADR (card's own call), no UI/i18n/help-topic surface (permissions
render as raw badge strings with no per-permission description map —
confirmed via `web/ui/pages/plugins_store.html`).

## Independent review findings (Opus, both fixed same-branch)

1. **MAJOR — fail-open on a non-value payload.** The original code
   type-asserted the raw `payload interface{}` against the concrete
   `SaleCompletedEvent` value; a future caller passing
   `*SaleCompletedEvent` instead would silently skip redaction entirely
   (`redactedPayloadBytes` stays `nil`, every subscriber gets the full
   payload). Reviewer confirmed this empirically before the fix. **Fixed:**
   redaction now round-trips the already-marshaled `payloadBytes` through
   `json.Unmarshal` into a fresh `SaleCompletedEvent`, which is immune to
   the original value's concrete type (`json.Marshal` produces identical
   bytes for a value or a pointer to it).
2. **MAJOR — unbounded audit-log growth on the tender path.**
   `CheckPermissionGranted` audits every denial; for this permission,
   "not granted" is the expected, permanent steady state for most
   `sale.completed` subscribers (plain ERP connectors never declare it),
   checked once per sale per such subscriber — reviewer measured 10 sales
   → 10 `permission_denied` rows for one ungranted subscriber, i.e.
   unbounded growth in the same `audit_log` table GoBD-relevant journal
   entries live in, at production sale volumes. Diverges from the
   `sales:read`/`inventory:read` precedent, which runs once per on-demand
   export request, not once per sale. **Fixed:** goes straight to
   `data.PluginRepo.CheckPermission` (the same primitive
   `CheckPermissionGranted` wraps), skipping the audit write for this one
   permission; a genuine DB error still fails closed (redacts) but logs a
   `warning:` line instead of an audit row, matching the neighbouring
   `auditEventWithDB`/`auditDispatchWithDB` failure-handling style a few
   lines below. Locked in by
   `TestEventBus_SaleCompleted_ReconciliationDenialNotAudited` (5 sales →
   0 audit rows) — confirmed failing against the pre-fix code (5 rows) in
   a personal TDD spot-check before restoring the fix.

Minor findings folded in while the file was open: the `eb.mu` reentrancy
comment now names the new `data.PluginRepo.CheckPermission` call
alongside the existing `CheckPermission`/`WasmRuntime.HandleEvent`
callees it documents as DB/wazero-only; a mixed granted/ungranted
single-`publish()`-call test
(`TestEventBus_SaleCompleted_MixedSubscribers_NoAliasing`) was added —
reviewer had already probed for a shared-backing-array aliasing bug and
found none, but nothing previously guarded the regression.

## TDD spot-checks (both done personally, both genuine)

- Reverted only `internal/plugins/ipc.go` (kept the new test file):
  `TestEventBus_SaleCompleted_RedactsCardFieldsWithoutPermission` and
  `TestEventBus_SaleCompleted_DeclaredButNotGrantedStillRedacted` fail
  (full PAN/auth code visible); the granted-path and non-sale-event tests
  correctly pass either way. Restored.
- Reverted just the MAJOR-2 fix (put `CheckPermissionGranted` back):
  `TestEventBus_SaleCompleted_ReconciliationDenialNotAudited` fails (5
  audit rows for 5 sales, expected 0). Restored.
- Reviewer independently reverted `ipc.go` in their own isolated worktree
  and confirmed the same two redaction tests fail — an independent
  confirmation, not just a rerun of mine.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...`, `gofmt -l` — clean.
- Full `go test ./...` — green (all packages).
- All guard scripts green: `guard-data-access.sh`,
  `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`, `guard-i18n.sh`,
  `guard-compliance-claims.sh`, `guard-docs-shots.sh` (unaffected — no UI
  surface touched).
- Reviewer additionally verified no bypass surface exists via the
  `sales:read` export ledger (`ExportSaleRow`/`ExportSalePayment` carry no
  card fields) and that `InsertPluginPermissions` inserts `granted=0` by
  default, so declaring `payments:reconciliation` does not auto-grant it
  (a manual/sideloaded install still needs an explicit merchant grant;
  a marketplace install auto-grants declared permissions only after
  Ed25519 verification + review, per ADR-0006 — consistent with every
  other permission, not a special case introduced here).

## Verdict

Safe to merge. Both MAJOR findings fixed and regression-tested
(independently reproduced as real bugs before the fix, confirmed gone
after); no BLOCKER found. Redaction happens before any bytes reach an
ungranted subscriber, on both `Blocking` and `NonBlocking` dispatch
modes, with no aliasing bug and correct `eb.mu` lock discipline.
