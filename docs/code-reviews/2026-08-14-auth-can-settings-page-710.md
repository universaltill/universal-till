# Code review: Auth `Can()` sweep — Settings page (ut-docs#710)

**Date:** 2026-08-14
**Author (Dev):** Sonnet (subagent, fresh context)
**Reviewer (independent):** Opus (subagent, fresh context, worktree-isolated)
**Card:** universaltill/ut-docs#710, successor of #555, same established
mechanism as #554/#706/#707/#709.

## What shipped

Replaced all `isManagerOrAuthOff(r)` gate call sites in
`internal/pages/settings_page.go` — 19 handler gates plus the `isManager`
template-data flag (20 sites total) — with `canPerform(d, r, "settings")`,
the `role_permissions`-table-backed helper already defined in
`internal/pages/authz.go`. Uses the existing `settings` catalog action
(migration `internal/db/migrations/039_role_permissions.sql`) — no new
migration. `isManagerOrAuthOff` itself is untouched (other not-yet-
converted files still call it), just unused within this file now.

Untouched, correctly out of scope: the function definition itself;
`POST /api/settings/exit-to-os` (gated by a live manager PIN check, not
this gate); `/api/settings/theme`, `/ui-scale`, `/osk` (never gated,
per-operator UI prefs).

## Independent review findings

- **Fixed — nit:** `gofmt` alignment drift in the new
  `TestSettingsEndpoints_RoleMatrix` table (`settings_page_test.go`,
  two struct literals) — introduced by this diff, `main` was gofmt-clean.
  Applied `gofmt -w`.
- **Noted, not fixed (pre-existing, out of scope):** `internal/pages/
  eod_api_test.go`, `external_api_test.go`, `import_bkp_page_test.go` are
  also gofmt-dirty on `main` already — untouched by this diff, left as-is.
- **Noted, not fixed (accepted, future follow-up):** `setup_page_test.go`'s
  `newFullAuthDeps` permission-catalog fixture seeds only migration 039's
  seven original actions, not 042/043/044's later additions. Currently
  harmless (the only `canPerform` action reachable through that fixture's
  registered routes is `settings`), but noted for whoever next converts a
  handler in `registerAuth`/`registerSetup` to a newer action — belongs
  with the #520 custom-role follow-up, not this card.
- **Noted, not fixed (accepted, pre-existing, not a regression):**
  `POST /api/settings/upsert` / `POST /api/settings/save` are generic
  key/value writers gated solely on `settings`; once #520 custom roles
  land, a role holding `settings` without `sync_management`/
  `data_management` could write those namespaces' keys through `upsert`,
  side-stepping #707's finer-grained gates. Identical under the old
  `isManagerOrAuthOff` gate — not introduced by this diff — flagged for
  the #520 design, not actioned here.
- **No finding** on: call-site completeness (re-grepped independently,
  zero `isManagerOrAuthOff` remaining outside the function definition),
  action mapping (every site cross-checked against the `#554` catalog —
  `settings` fits all 20; the `/api/enrol/*` marketplace-registration
  handlers were specifically checked against `#707`'s `sync_management`
  and confirmed to be a different concern), shared-fixture blast radius
  (all six other `registerSettings` call sites in tests that lack the new
  `AuthSvc` wiring were individually audited — each short-circuits before
  touching it), money/SQL/kiosk-engine/i18n (none touched), and secrets/
  real client names (none present).

## Verified beyond automated tests (independent re-verification, not just Dev's word)

Reviewer performed its own revert-then-restore TDD checks, in an isolated
git worktree (never the shared checkout), on 3 sites the Dev's own report
did not test:

- `GET /api/enrol/devices` + `POST /api/settings/telemetry`, reverted
  together → exactly the two expected `super_admin_past_gate` subtests
  failed, for the expected reason (`User.IsManager()` doesn't recognize
  `super_admin`); `cashier`/`manager`/`admin` rows unaffected.
- The `isManager` template flag, reverted alone →
  `TestSettingsPage_HidesManagerOnlyCardsFromCashier`'s `super_admin` case
  failed as expected.
- **Extra, beyond a plain revert:** deleted the gate blocks entirely (not
  just reverted to the old function) on `telemetry` and
  `remove-demo-catalogue`, to rule out a downstream-error false pass —
  both `cashier_denied` rows failed as they should, confirming the
  negative assertions have real teeth, not just a status-code coincidence.

All three experiments were restored and the full `internal/pages` package
re-ran green afterward; worktree confirmed clean before the review agent
exited.

Orchestrator (this session) independently re-ran, in addition to the
above: `go build ./...`, `go vet ./...`, `go test ./... -count=1` (full
repo, all ~40 packages green), `guard-data-access.sh`,
`guard-kiosk-engine.sh`, `guard-i18n.sh` — all green — and performed its
own separate revert-then-restore spot check (`POST /api/settings/save`,
line 751) confirming the `save/super_admin_past_gate` subtest fails when
reverted and passes when restored.

## Safe-to-merge verdict

**Yes.** Independent review found no blocker; the one nit (gofmt) was
fixed before merge. Behaviour is exactly preserved for cashier/manager/
admin; `super_admin` broadening is the one accepted, documented,
currently-inert behaviour change (#554/#555), consistent with every prior
successor in this sweep.

## Deferred / follow-up items

- Permission-catalog fixture drift in `setup_page_test.go` and the
  `upsert`/`save` generic-writer granularity gap — both belong with the
  #520 custom-role work, not filed as new cards here (pre-existing,
  unrelated to this card's scope).
