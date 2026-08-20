# Code review: wire user_management mutations onto manager-override elevation (ut-docs#795)

**Date:** 2026-08-20
**Card:** ut-docs#795 (split off ut-docs#781 / ut-docs#557)
**Complexity:** medium — build: Sonnet, review: Opus (fresh-context subagent, isolated worktree)

## What shipped

The 4 mutating, audit-writing `user_management`-gated handlers in
`internal/pages/users_page.go` — create user, set PIN, activate/deactivate,
change role — moved off a flat `canPerform(d, r, "user_management")` → 403
onto the existing generic manager-override-elevation mechanism
(`internal/pages/elevation.go`'s `checkOrElevate`/`renderElevationPrompt`,
dual-attribution audit via `InsertAuditElevated`), mirroring
`internal/pages/eod_api.go` (ut-docs#794) exactly. `GET /users` (read-only)
stays on the flat 403 — elevation is scoped to mutations, per
`checkOrElevate`'s own doc comment and ADR-0052 §2.

`POST /api/users/{id}/promote-super-admin` and every
`permission_management`-gated branch (super_admin grants/removals inside
`POST /api/users` and `POST /api/users/{id}/role`) are explicitly out of
scope (tracked separately by ut-docs#796) and are untouched — still reading
the session user via `canPerform`, never the resolved/elevated actor.

The `canManage`/"only admins change managers or admins" business rule is
evaluated against the **resolved** actor (the approver once elevated, the
session user otherwise) via a new `resolveActingUser` helper — using the
blocked session actor's role here would let a cashier's own scope limits
leak through after a legitimate approval, defeating the point of elevating.

Forms in `web/ui/pages/users.html` (pin/active/role/create) converted to
htmx; new `elevation.summary.user_*` + `users.saved` i18n keys added to all
4 locales; `web/help/{en,ar,fa,tr}/{elevation,users}.md` updated.

## Independent review — round 1 (Opus, isolated worktree)

Cleared, no findings: the authorization logic itself — `checkOrElevate`
wiring on all 4 handlers, no bypass of the old flat gate, `canManage`/
`resolveActingUser` design (verified as the correct analogue of
`checkOrElevate`'s own `svc.Can(approver, action)` re-check — elevating
never widens scope beyond the approver's own), `permission_management`
branches byte-identical, dual-attribution ordering correct, last-active-
admin/super-admin guards firing under both `allowed` and `elevated`, no
raw SQL outside the data layer, no real client/shop names, real (non-
English-copy-paste) translations in all 4 locales.

Found **2 blockers** (feature non-functional in a real browser despite
green Go tests) and several should-fix items:

- **Blocker 1**: `hx-on::after-request="if (event.detail.successful)
  window.location.reload()"` fired on the elevation-PROMPT response too
  (both the prompt and a real success are HTTP 200, and htmx's
  `successful` is true for any non-4xx/5xx) — the reload wiped the
  just-opened dialog before a PIN could be entered. No path to complete an
  elevation existed.
- **Blocker 2**: business-rule refusals (`users.error.role`,
  `pin_taken`, `last_admin`, `last_super_admin`, `role_change`, …) used
  non-2xx statuses. htmx does not swap a non-2xx response by default, and
  this app's `htmx:responseError` handler shows a generic "server error"
  banner instead of the real message — a regression from the pre-diff
  full-page-redirect behavior, where these rendered correctly. Go tests
  passed throughout because `httptest` asserts on `rec.Body`, which htmx
  never renders.
- Should-fix: untranslated raw role identifiers in approval summaries
  (S3), zero test coverage discriminating actor-vs-approver resolution
  for 3 of 4 handlers (S4), user manual not updated for the new behavior
  (S5). Also flagged out-of-scope-for-this-card: the elevation prompt is
  currently unreachable via the shipped UI because `GET /users` shares the
  same `user_management` gate as its mutations (S1), and the realistic
  override case — a `canManage` denial, not a `canPerform` denial — isn't
  elevated (S2), both pre-existing-pattern/scope questions, not
  regressions, tracked as follow-ups below rather than fixed here.

## Fix pass

- **Blocker 1 fixed**: a dedicated `X-UT-Response` response header
  (`elevation-prompt` / `ok` / `refused`, all three now HTTP 200) lets the
  client distinguish prompt-don't-reload / success-do-reload /
  refusal-show-message-don't-reload. Applied to all 4 forms in
  `users.html` and to the shared `elevation_prompt.html` dialog's own
  retry form (which previously had no `after-request` handler at all and
  stayed open over a stale row on success — should-fix S7, folded into
  this fix since it's the same code path).
- **Blocker 2 fixed**: every business-rule refusal in the 4 handlers now
  responds 200 + `X-UT-Response: refused` instead of 400/409/500 —
  same precedent `permission_settings_page.go`'s own lockout-guard branch
  already documents for the identical trap.
- **S3 fixed**: new `userRoleLabel(locale, role)` helper (mirrors
  `permission_settings_page.go`'s `permissionChangeSummary` pattern),
  used at every summary call site that names a role.
- **S4 fixed**: added `*_ActorResolutionMatters` tests for create/pin/
  active (change-role already had one), each using an admin approver +
  manager/admin target via a blocked cashier session — the scenario where
  session-actor-vs-approver actually changes the outcome.
- **S5 fixed**: `web/help/{en,ar,fa,tr}/users.md` updated with a new step
  describing the approval-prompt behavior and a link to the shared
  `elevation` help topic.

## Independent review — round 2 (Opus, isolated worktree, scoped to the fix)

Per this pipeline's standing rule, a second round is earned only by a
first-round blocker and is scoped to the fix, not a full re-review — the
diff (`beebca2..67310aa`) was confirmed to touch only response-writing,
the new header, `userRoleLabel`, doc comments and tests; zero lines in the
already-cleared `checkOrElevate`/`canManage`/`resolveActingUser` control
flow. Verified against the actual vendored htmx source (not the fix's own
description): `event.detail.xhr` is populated before `afterRequest` fires
and headers are readable there; `getResponseHeader` is case-insensitive
per spec and the code never string-compares the header *name* (only its
value); the dialog's own retry form correctly stays open on a repeated
`elevation-prompt` (wrong PIN) and only closes/reloads on a terminal
outcome. All 14 originally-flagged error call sites confirmed routed
through the fixed `usersRespondError`; no leftover `http.Error`/
`http.Redirect` inside the 4 handler bodies. S3/S4/S5 confirmed actually
done (not just claimed) — S4 independently re-verified by reverting
`resolveActingUser` to always return the session actor and confirming the
new tests fail with the real, expected assertion error, then restoring.

**Verdict: safe to merge**, with two non-blocking follow-ups:

1. The four genuine-internal-error branches in the role-change handler
   (`BeginTx`/`SetUserRole`/audit-write/`Commit` failures) had gone from a
   distinct 500 to the same silent 200 "refused" response as any business
   rule, with the underlying error discarded and no log line — a real DB
   failure would have been indistinguishable from "you tried to demote the
   last admin," with nothing in the logs either. **Fixed in this same
   branch** (not deferred): `logging.L().Errorf` added on all 5 genuine-
   error branches (create, plus the 4 role-change tx steps) before
   responding, so a real failure is still visible server-side even though
   the client-facing response is now a friendly 200.
2. Test-coverage gap: no test asserted the `X-UT-Response: elevation-prompt`
   / `ok` header values themselves — only `refused` had a dedicated
   assertion (`TestUsersPage_BusinessRuleRefusal_IsHtmxSwappable`), so
   deleting the header from `renderElevationPrompt`/`usersRespondOK` (which
   would silently reinstate Blocker 1, or stop a real success from ever
   reloading) would have left the whole suite green. **Fixed in this same
   branch**: added header assertions to `TestUsersPage_CreateUser_
   ElevationFlow`'s prompt and elevated-success subtests.

## What was verified beyond automated tests

- **Live-driven, real HTTP, both rounds**: booted the actual binary
  against a real seeded SQLite DB with real PIN hashes (`auth.HashPIN`),
  drove it with `curl` as a logged-in cashier through the real `net/http`
  mux (not `UT_AUTH=off`, not `httptest`) — confirmed the elevation
  prompt response (200, `X-UT-Response: elevation-prompt`), a correct
  approver PIN completing the action (200, `X-UT-Response: ok`, dual-
  attribution audit row with `actor_id` = approver, `blocked_actor_id` =
  the originally-blocked cashier), an invalid PIN re-prompting, and a
  business-rule refusal responding 200/`refused` with the real message.
- **Visual check** (Tester step, before either review round): screenshot
  of `/users` in English (LTR) and Persian (RTL), light theme, 1280×900 —
  new message spans and htmx-converted forms render cleanly with no
  overlap/cutoff, RTL mirrors correctly. **Not covered**: dark theme (no
  client-side `data-theme` toggle mechanism found on this page to drive
  one), and a screenshot of the elevation dialog itself mid-flow (only
  the kiosk service-identity row was present in the auth-off seed data
  used for the screenshot pass — the dialog's own shared markup is
  unchanged from the already-shipped, already-reviewed #794/#557 partial).
- **TDD re-verified independently, not taken on the implementer's word**,
  both rounds: round 1 reverted the `checkOrElevate` wiring on `POST
  /api/users` and confirmed the elevation test failed with the real
  pre-fix error, then restored; round 2 reverted `resolveActingUser` to
  always return the session actor and confirmed all 3 new
  `ActorResolutionMatters` tests failed with real assertion errors
  (`only admins create managers or admins` / `cannot manage this user`),
  then restored.

## Explicitly deferred (follow-up backlog cards, not fixed here)

- **S1** — the elevation prompt is currently unreachable through the
  shipped UI: `GET /users` gates on the same `user_management` action as
  its own mutations, so a session blocked from the mutations can't load
  the page that contains their forms in the first place. This mirrors an
  existing accepted pattern (`permission_settings_page.go` has the same
  shape, accepted under #557) rather than a regression introduced here,
  and the card's own acceptance criteria are met as literally written —
  but it means this card's real-world observable behavior change is nil
  until a follow-up addresses page-vs-mutation gate separation.
- **S2** — the realistic override scenario on this page (a manager
  blocked by `canManage` from touching an admin) is not elevated — only a
  flat `canPerform` denial is, per the card's own literal acceptance
  criteria ("offers in-place elevation on a `canPerform` denial"). Worth
  a product decision on whether `canManage` denials should also elevate.

## Safe-to-merge verdict

**Safe to merge.** Full gate green (`go build`, `go vet`, `go test ./...`,
all 6 guard scripts, `gofmt -l`) after the fix pass and the two follow-up
items folded in above. No open blockers.
