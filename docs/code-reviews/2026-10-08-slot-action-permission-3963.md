# Review: slot panel actions run under `ui:slot:<slot>` + host gate (ut-docs#3963)

Date: 2026-10-08 · Card: ut-docs#3963 (complexity:easy) · Branch:
`fix/3963-slot-action-permission`

## What shipped

- `internal/pages/plugin_view.go`: a POST whose `HX-Target` is a content
  slot panel (`inSlot`) on an entry that declares a slot is checked against
  `ui:slot:<entry.Slot>` instead of `ui:page`, so a slot-only plugin's
  forms and uploads run. `/plugin/` routes need only a session, so the same
  post also needs the slot's host-screen gate (`pluginSlotGates`), as
  `GET /ui/slot/{slot}` does; missing gate or no gate (setup wizard) → 403
  before the form is read (nothing staged), plugin not asked. Page actions
  (no or forged slot target, entry without a slot) and GET view asks keep
  `ui:page`. `askPluginAction` takes the permission as an argument.
- Tests (`plugin_slot_test.go`, `plugin_view_upload_test.go`): slot-only
  action runs, page action without `ui:page` still refused; another slot's
  grant or `ui:page` alone refused for a panel action; host gate (cashier
  403 / admin 200); fail-closed for a gate-less slot; multipart upload into
  a slot panel (handles delivered, answer targets the panel, upload
  released) — the coverage gap the card named.
- Docs: `docs/plugin_guidelines.md`; ut-docs `reference/plugin-views.md`
  (Content slots → Actions) in a companion PR.

Design choice: the card offered "refuse at install a slot entry without
`ui:page`" or "accept the slot permission on the action path". The second
is least privilege (no page grant just to make a panel work) and also
covers an operator revoking `ui:page` after install, which an install-time
check cannot.

## Review

Independent review by a different model (Opus 5.5; Dev was Sonnet 5.5),
fresh context, separate worktree.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Blocker | `TestPluginViewUpload_IntoSlotPanel_3963` 403s unless `UT_AUTH=off` (the new host gate runs; `newViewHarness` doesn't disable auth) | Fixed: test sets `UT_AUTH=off`; the gate has its own test |
| 2 | Minor | A slot entry whose plugin also holds `ui:page` can be posted as a page action (no/forged `HX-Target`) by a session lacking the host permission | Not a regression (same before); doc wording scoped to "posted from the panel"; follow-up Backlog card filed (gate every slot entry's actions by its host screen) |
| 3 | Minor | Fail-closed paths not pinned | Fixed: `TestPluginSlot_ActionWithoutGateRefused_3963` (gate runs before `readPluginViewForm`, so nothing is staged — by construction) |
| 4 | Nit | 403 body is plain text, unlike `requirePage` | Accepted: matches `GET /ui/slot/{slot}`'s 403; htmx doesn't swap 4xx |
| 5 | Nit | ut-docs reference needs the same wording | Done in the companion ut-docs PR |

Security questions checked by the reviewer with no problem: the permission
and gate come from server-side `entry.Slot`, never from the panel id; a
forged id only picks where the answer is drawn; GET view and job-poll paths
unchanged.

## Verified

- TDD: each `*_3963` test fails against the pre-fix `plugin_view.go`
  (reviewer re-ran in a separate worktree; host-gate and fail-closed tests
  re-verified by the orchestrator) and passes after.
- `gofmt -l .` clean, `go build ./...`, `go vet ./internal/pages/...`,
  `go test ./internal/pages/...`, guards: data-access, i18n,
  page-http-error, core-neutral, kiosk-engine, no-inline-handlers,
  netaccess. `deadcode-baseline` and `golangci-lint` could not run locally
  (tool binaries built with an older Go than the repo's 1.27) — CI covers
  them; no new functions were added.
- No UI markup changed (an existing panel now works instead of showing the
  "can't be shown" notice), so no screenshot pass; nothing visual looked at.

Verdict: safe to merge.
