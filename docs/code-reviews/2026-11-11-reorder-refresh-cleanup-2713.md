# Review — Designer category reorder: drop the dead refresh hook (ut-docs#2713)

Date: 2026-11-11 · Card: ut-docs#2713 · Reviewer model: Opus 5.5 (author:
Sonnet dev agent) · Branch: `chore/2713-reorder-dead-refresh` · Companion
docs PR: ut-docs `docs/2713-list-dialog-reorder-pattern`

## What shipped

- `internal/pages/designer_categories_api.go`: `POST
  /api/designer/categories/reorder` answers a bare `204` with no
  `HX-Trigger: buttons-changed`. Its only caller is the `fetch()` in
  `web/public/list-reorder.js`, and `fetch()` never acts on `HX-Trigger`, so
  the header was already dead at runtime. Create, update and active still
  answer `buttons-changed`. The file header and the route comment are fixed.
- `web/public/list-reorder.js`: removes the `data-reorder-refresh` contract,
  `refreshAfterSave` and its call. No template sets that attribute.
  `list-reorder:saved` / `list-reorder:reverted` are still dispatched, and the
  Designer updates its preview from `list-reorder:saved`
  (`designer.html:168`, `applyCategoryOrder`).
- Comment-only: `web/public/app.css` (~6789) and
  `e2e/tests/designer-wysiwyg-2174.spec.ts` (header, keyboard-reorder step).
- Tests: `TestDesignerCategoriesAPI_CatalogManagementGate` gets a per-route
  `wantTrigger` (empty for reorder). The new `TestListReorderJS_NoRefreshHook`
  checks that the hook is gone and both events are still dispatched.

## Findings

| # | Sev | Where | Finding | Disposition |
|---|---|---|---|---|
| F2 | Low | `designer_categories_api.go:40-44` | The file header still said every route answers `HX-Trigger: buttons-changed`. | **Fixed.** |
| F3 | Low | `e2e/tests/designer-wysiwyg-2174.spec.ts:10-12,196-197` | The comments described reorder as sending `buttons-changed` and re-rendering. Stale since #3074. | **Fixed** (comment only). |
| F1, F4–F8 | — | ut-docs doc | Doc findings: see the ut-docs record `code-reviews/2026-11-11-list-dialog-reorder-pattern-2713.md`. | — |

Safety of dropping the header was checked against every `buttons-changed`
listener: the buttons.html root `hx-trigger`, app.js, sell-screen-watch.js
(catalog-version poll, #2765), the catalogsync forwarder (only relays
headers, and the caller is still `fetch()`), demo mode (allow-list only),
plugins and ut-cloud (no caller). Nothing relied on it.

## Verification

- TDD re-verified twice (Tester and Reviewer, in a detached worktree).
  Reverting the two prod files makes `TestListReorderJS_NoRefreshHook` and
  `.../reorder` fail with the claimed errors; restoring them makes both pass.
- `go build ./...`, `go vet`, `gofmt -l .` are clean. `go test
  ./internal/pages/... ./internal/ui/...` is green. `golangci-lint run
  ./internal/pages/...` reports 0 issues. The applicable build-job guards
  pass (i18n, no-inline-handlers, deadcode-baseline-growth, data-access,
  fixed-dates, rtl-logical-css and others).
- e2e (Chromium, `--trace off`): `category-list-drag-2699`,
  `designer-drop-no-reload-3074`, `designer-wysiwyg-2174` and
  `categories-record-dialog-2010` give 42 passed, 0 failed. Order survives
  `page.reload()` for mouse and touch drag, Alt+Arrow and Move. The Designer
  reorders in place with no `/ui/buttons` refetch, and a refused move puts
  the rows back.

Pre-flight: review record updated (this file); web/help + docs-shots n/a (no
visible change; app.css edit is a comment); reference/ updated (ut-docs
list-and-dialog-pattern.md, companion PR); README n/a; en.json n/a; ADR n/a;
cross-repo mirrors n/a; follow-up → ut-docs#6046 (five pencil screens).

## Verdict

PASS. Merge after CI is green.
