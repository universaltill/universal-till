# Code review: manager-override-in-place elevation flow (ut-docs#557)

**Date:** 2026-08-16
**Author (build):** Sonnet subagent (complexity:medium)
**Reviewer:** Opus subagent, independent (isolated worktree, no prior context of the build), plus a Sonnet fix pass on the findings, both re-verified personally before merge.

## What changed

Implements a generic manager-override-in-place elevation mechanism: when a `canPerform`-gated
mutating, audit-writing action is denied, the operator is offered an in-place PIN re-auth
instead of a flat 403. A valid approver PIN completes the original action as that approver,
and the audit trail records **both** the originally-blocked user and the approving user.
Design rationale and the full decision record are in
[ADR-0052](../../../ut-docs/adr/0052-manager-override-elevation-pattern.md) (`ut-docs` repo).

- New `internal/pages/elevation.go`: `checkOrElevate(dp, r, action, pin) elevationCheck`.
  No stored elevation token — single-use/short-lived falls out by construction, since a
  fresh PIN is verified via the existing `AuthSvc.AuthorizeManager` (shared device lockout)
  on every elevated request.
- New migration `049_audit_dual_attribution.sql`: additive nullable `audit_log.blocked_actor_id`.
  `internal/data/pos_repo.go`: new `InsertAuditElevated`; existing `InsertAudit` delegates to
  a shared unexported `insertAudit`, byte-identical signature, unchanged behavior for all 81
  existing call sites. `AuditEntry.BlockedActorID` threaded through `ListAudit`/`ListAuditForExport`.
- New `web/ui/partials/elevation_prompt.html`, swapped out-of-band into a shared
  `<dialog id="elevation-modal">` placeholder in `web/ui/layouts/base.html` (see Independent
  review findings below for why — not the triggering action's own `hx-target`).
- Wired into 3 real, already-`canPerform`-gated sites: `backup_api.go` (`POST /api/backup/now`,
  `data_management`), `sync_api.go` (`POST /api/sync/promote`, `sync_management`),
  `permission_settings_page.go` (`POST /api/users/permissions`, `permission_management`).
- i18n: `elevation.*` keys across all 4 shipped locales (en/ar/fa/tr — hand-translated, the
  homelab Ollama translation endpoint was unreachable from this sandbox; flagged for a native
  speaker to spot-check eventually, not blocking).
- Help topic: `web/help/{en,ar,fa,tr}/elevation.md`.

## Scope deviation from the original ask (found, not hidden)

BA/Architect's brief named `refund`/`void`/`price_override`/`cash_adjustment`/`eod_report`/
`user_management`/`settings` as the target action catalog. Grepping the real code at
implementation time found **none of `refund`/`void`/`price_override`/`cash_adjustment` are
gated by `canPerform` anywhere** — never reachable for this card. `eod_report` (actor-less
shared `generateEOD`, used by an unattended scheduler), `user_management` (plain non-HTMX
forms), and `settings` (~30 sites, only one calls `InsertAudit`) each need materially
different work, not a simple wiring pass — split off as
[ut-docs#781](https://github.com/universaltill/ut-docs/issues/781). ADR-0052 was corrected
post-review to record this rather than leave the original aspirational catalog standing.
The mechanism itself, and the 3 sites it's wired to, are complete and independently verified.

## Independent review (Opus, isolated worktree)

Full findings in the pipeline transcript; summarized here. Verified personally: build/vet/full
`go test ./...`/all guards green; TDD claim on the Tester's migration-replay fix independently
re-verified by reverting the fix, confirming the exact claimed `duplicate column name` failure,
then restoring it.

**Blockers found and fixed:**
1. **Nested `<form>` silently dropped.** The dialog was originally swapped directly into the
   triggering action's own `hx-target` (e.g. `tills.html`'s `#promote-msg`), which sits inside
   the page's own `<form>`. Per the HTML fragment-parsing algorithm, a `<form>` start tag is
   ignored while a form element pointer is already open — the dialog's inner `<form>` (PIN
   input, hidden fields, submit) was silently absorbed into the outer page form. It happened
   to "work" for `/api/sync/promote` by coincidence (same URL/target as the outer form) but
   was structurally wrong and would break on any other page. **Fixed**: the dialog now renders
   as an htmx out-of-band swap (`hx-swap-oob="true"`) into one shared `<dialog id=
   "elevation-modal">` placeholder in the base layout, matching how `#hold-modal`/`#pfand-modal`
   already live outside any form. Pinned by two new tests using the real
   `golang.org/x/net/html` parser — one proving the old shape drops the inner form, one proving
   the new output doesn't.
2. **`showModal()` breaks the on-screen keyboard on kiosk hardware.** This app's own CSS
   documents that `showModal()` makes everything outside the dialog inert, including the
   custom OSK (`#osk`, appended to `<body>`, not inside the dialog) that kiosk Pis depend on —
   and the PIN field is exactly the input type the OSK targets. Every other typed-input dialog
   in this app (`#hold-modal`, `#pfand-modal`) already deliberately uses `.show()` for this
   reason; the elevation dialog was the one exception. **Fixed**: switched to `.show()`,
   matching established precedent.

**Real-but-deferrable, fixed anyway (cheap, and directly on this card's own promise):**
3. **Approver couldn't see what they were approving** — the action/target traveled only as
   hidden fields. Worst for the permission-management site, where the elevated action is a
   *permanent* role/permission grant, not a transient one. **Fixed**: added a visible,
   human-readable summary line to the dialog; the permission-settings site names the actual
   role, action, and grant/revoke direction being changed.
4. **Elevation checked before input validation** — a manager could burn a PIN entry on a
   request that then 400s anyway. **Fixed**: validation now runs first in both affected
   handlers (also moved `permission_settings_page.go`'s self-lockout guard ahead of elevation
   for the same reason — a hard block regardless of approver).
5. **Minor**: `elevation.pin_label` renamed to "Manager PIN" to match every sibling key's
   convention (was bare "PIN", inviting a cashier to type their own). The nil-`AuthSvc`
   fallback no longer mints a throwaway `auth.Service` (would silently reset the lockout
   counter if ever reachable) — now fails closed.

**Confirmed safe, no fix needed:**
- `checkOrElevate` re-checks the **approver's** own permission for the action (not the
  session user's), using the same action string the handler already gated on — a manager PIN
  against `permission_management` is correctly refused. No stored token means no cross-action
  or cross-target replay is structurally possible.
- `InsertAudit`'s refactor is behaviorally identical for all existing callers (signature
  unchanged, delegates with `blockedActorID=""` → NULL); `ListAudit`/`ListAuditForExport` add
  the new column consistently, last position, in both the SELECT and the `Scan()`.
- No raw SQL outside `internal/data`/`internal/db`; no new disk writes (so `MkdirAll`/
  `paths.Data` are N/A here); no hardcoded English or literal left/right in the new template;
  dark-theme-safe (CSS custom properties only).

**Accepted as documented deferral (not fixed, not silent):** `AuditEntry.BlockedActorID` isn't
yet surfaced in the audit page UI or CSV export — the DB layer carries it, reporting is a UI
polish follow-up per ADR-0052's own Consequences section, and the help topic says so honestly.

## Verification

- `go build ./...`, `go vet ./...` — clean.
- Full `go test ./...` — all packages pass (found and fixed 8 real migration-replay test
  fixtures during Tester's pass — a genuine bug in `internal/db/{barcode_seed_test.go,
  dead_seed_test.go,demo_seed_migration_test.go}` missing a `DROP COLUMN blocked_actor_id`
  rewind step, caught only by running the full suite, not the touched packages alone).
- `scripts/ci/guard-data-access.sh`, `guard-i18n.sh`, `guard-help-topics.sh`,
  `guard-docs-shots.sh` (regenerated via `make docs-shots`, real Chromium run, 76/76 pass) —
  all green.
- Driven, real HTTP server + real SQLite DB + real users (Tester pass): confirmed the
  elevation-prompt response on denial, dual-attribution audit row on a valid approver PIN
  (`actor_id`=approver, `blocked_actor_id`=blocked user), fail-closed on a wrong PIN, and the
  shared device lockout genuinely engaging after 5 wrong attempts.
- Translations (ar/fa/tr): spot-checked for script/character correctness and absence of
  leftover English or obvious machine-translation artifacts — not confirmed by a native
  speaker, noted as a non-blocking follow-up.

**Verdict: safe to merge**, with the scope reduction and deferred items above tracked
explicitly (ut-docs#781, audit-UI-surfacing follow-up) rather than silently dropped.

Closes universaltill/ut-docs#557.
