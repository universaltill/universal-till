# Review — plugin view text runs in its own direction (ut-docs#3947)

**Date:** 2026-10-22 · **Built by:** Opus 5.5 · **Independent review:** Sonnet 5.5 (different model, fresh context, own worktree).

## What shipped
- `web/ui/partials/pluginview/document.html`: `dir="auto"` on the plugin's block text: view title, heading, text, notice, and empty-state title and body.
- `web/ui/partials/pluginview/data.html`: `dir="auto"` on the QR caption. `<bdi>` wraps table text cells, every column header, list items, stat-tile labels and wizard step labels, so each of those elements keeps the page's alignment and list marker. An empty text cell gets no `<bdi>`, so `table-cards.js` can still mark it blank.
- Number and money cells are unchanged; #3891 covers them.
- Test `internal/pages/pluginview_dir_3947_test.go` renders every literal-bearing component in `fa` and checks two things: the element holding each literal carries `dir="auto"` or is a `<bdi>`, and money cells and empty cells are not isolated.
- Three existing tests now expect the new markup: `plugin_view_3874_test.go`, `plugin_slot_test.go` and `plugin_slot_sections_test.go`.
- `web/help/img/manifest.json`: surface hash refreshed only. No docs-shot renders a plugin view (`e2e/tests-docs/docs-shots.spec.ts`), and English text in an LTR page renders identically.

## Findings (Sonnet)
| # | Sev | Finding | Handling |
|---|---|---|---|
| 1 | High | Three existing pages tests asserted the old markup | Fixed: the assertions now expect the new markup, and escaping (`&lt;b&gt;`) is still checked |
| 2 | Low | An empty text cell rendered `<bdi></bdi>`, so `table-cards.js` no longer saw it as blank (phone cards) | Fixed: `<bdi>` only around a non-empty value, with a test |
| 3 | Info | Out of scope: form labels, options and buttons in `controls.html`, `Poll.Message`, paging text | Backlog follow-up card |
| 4 | Info | Weak money-cell assertion in the new test | Fixed |

TDD was re-verified by the reviewer: with the old partials the new test fails for every literal, and with the new ones it passes.

## Verified beyond automated tests
I rendered the fragment in a `lang="fa" dir="rtl"` page with `web/public/app.css` at 1024×600 and screenshotted it before and after the change (Chromium). Before, every Latin literal showed its full stop at the wrong end: ".Run end of day first", ".STOCK SYNC", ".Oat milk". After, the punctuation is correct in the title, text, notice, list, tile label, table headers and cells, and wizard steps. Table columns and list bullets still follow the RTL page.

## Gate
`gofmt`, `go vet`, `golangci-lint` (0 issues), `go build ./...`, `go test ./...` (all packages pass except `internal/plugins`, which this change does not touch and which hit the default 10-minute timeout in this container; CI runs it), and every guard in `ci.yml`'s build job pass. `guard-shellcheck-version` was skipped because this container has no shellcheck binary.

## Safe to merge
Yes.
