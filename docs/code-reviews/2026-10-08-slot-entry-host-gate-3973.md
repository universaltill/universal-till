# Review: a slot entry's own route needs the slot's host gate (ut-docs#3973)

Date: 2026-10-08 · Card: ut-docs#3973 (complexity:easy, security) · Branch:
`fix/3973-slot-entry-host-gate`

## What shipped

- `internal/pages/plugin_page.go` `servePluginEntry`: an entry that declares
  a content slot is checked against that slot's host-screen gate
  (`pluginSlotGates`) on **every** request to its route, before any
  dispatch: GET/HEAD page, htmx fragment, job poll, and a POST with no or a
  forged `HX-Target`, for view and view-less entries alike. If the gate is
  missing or fails, the till renders `requirePage`'s translated 403 error
  page, with the rail intact, and does not ask the plugin. Fail closed:
  `setup.wizard.steps` has no gate, so its route is 403 for everyone, while
  the wizard's inline draw (`renderSetupSlot`) is unaffected.
- `internal/pages/plugin_view.go`: #3963's POST-only, panel-targeted gate
  is removed, because the new gate subsumes it. The permission choice is
  unchanged: `ui:slot:<slot>` for an action into a panel, `ui:page`
  otherwise.
- Tests (`plugin_slot_test.go`):
  - `TestPluginSlot_RouteNeedsHostGate_3973`: the plugin holds `ui:page`
    too, so only the gate can refuse. A cashier gets 403 on all six request
    shapes, the plugin is not asked, and a navigation gets the error page.
    An admin gets 200 with the right ask event. A view-less slot entry is
    gated too.
  - `TestPluginSlot_UngatedSlotRoute403_3973`: the wizard slot's route
    answers 403 for an admin, and the inline draw still works.
- Docs: `docs/plugin_guidelines.md` (Content slots), plus ut-docs
  `reference/plugin-views.md` in a companion PR.

Design choice: the card offered two options: always gate a slot entry
(fail closed), or keep the page path open and document it. We chose to
always gate, the card's recommended default. A slot panel is a fragment of
a gated core screen, so its plugin's view must not be reachable by a role
that screen refuses.

## Review

Independent review by a different model (Opus 5.5; Dev was Sonnet 5.5),
fresh context, separate worktree. Verdict: safe to merge.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Minor | The new 403 on a plain GET navigation was bare text/plain "Forbidden", with no rail and no translation | Fixed: `httpx.RenderError` with `common.error.manager_or_admin_required` (the `requirePage` precedent, #3079). The test asserts the error page. |
| 2 | Minor | `/menu` still shows a tile for a slot entry, so a cashier, or anyone for the wizard slot, taps it and gets a 403 | Follow-up Backlog card ut-docs#3982 (menu tiles are a separate surface; not widening this PR) |
| 3 | Nit | A plugin that puts `slot` on its `docs` entry now gets a Docs button that 403s for viewers without the gate | Accepted: an unlikely manifest shape, and the 403 is the correct answer for that viewer |
| 4 | Nit | The comment and docs said "every request", but view-less slot entries (served by `renderPluginPage`) were not gated | Fixed: the gate moved up into `servePluginEntry`, covering both paths. A test covers the view-less entry. |

Bypass hunt (reviewer): every route into the plugin's view goes through
`servePluginEntry` (the `/plugin/` catch-all and the index catch-all),
which now gates before the job poll, upload staging, `ui.action.ask` and
`ui.view.ask`. A denied request stages no files. CSP and nosniff are set
before the gate, so the 403 carries both. The diff writes no files and
uses no cwd-relative paths.

## TDD verification

- Reviewer: restored the pre-change `plugin_view.go`; both `_3973` tests
  failed for the right reason (cashier 200 and the plugin asked on all six
  shapes; admin 200 on the wizard route). With the change restored, they
  pass, along with every `_3963` test.
- Orchestrator, after the review fixes: stashed the production change back
  to the reviewed WIP. The new assertions failed ("cashier page 403 …
  want the translated error page"; "view-less slot entry = 200, want 403").
  With the change restored, they pass.

## Gate

`gofmt -l .` clean; `go build ./...`; `go vet ./internal/pages/`; full
`go test ./...`. Guards: data-access, i18n, core-neutral, page-http-error,
kiosk-engine, netaccess, no-inline-handlers, no-showmodal, help-topics, all
green. No new locale keys and no UI change beyond the standard error page,
so no help topic or screenshot is affected.

## Deferred

- ut-docs#3982: hide a slot entry's menu tile from roles its gate refuses.
