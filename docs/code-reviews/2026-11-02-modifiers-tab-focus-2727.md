# Review: Modifiers tab keeps keyboard focus across swaps (ut-docs#2727)

Date: 2026-11-02 · Branch `fix/2727-modifiers-tab-focus` · Lane `lane:cloud-41`
Built by Sonnet (subagent); independent review by Opus in a fresh context.

## What shipped

In the item editor's Modifiers tab, **Remove from this item**, **Attach**
and **Skip / Use again** swap `#item-modifiers-list` with htmx `outerHTML`.
None of the buttons had an id, so htmx 1.9.12 could not put focus back
after the swap and a keyboard or switch user landed on `<body>`.

- `web/ui/partials/modifier_group_admin.html`: stable ids, so htmx restores
  focus to the re-rendered control:
  - `modifier-detach-<group>`;
  - `modifier-inherited-toggle-<group>`, one id for both Skip and Use again;
  - `modifier-attach-submit`.

  The direct-groups heading gets `id="item-modifiers-heading" tabindex="-1"`.
- `web/ui/pages/catalog.html`: if the control that had focus is gone after a
  mutation, focus moves to that heading. That happens when a detach removes
  the row, or when an attach empties the picker.
  - It fires only for a request that targeted `#item-modifiers-list`, and
    only when that request's own xhr settles.
  - It fires only when focus is really lost (`body`, null or detached).
  - The tab-open load targets `#item-modifiers-host`, so opening the tab
    never moves focus.
  - The heading is not an input, so no on-screen keyboard opens.
- Tests:
  - `internal/pages/catalog/item_modifiers_focus_2727_test.go` checks the
    ids, that the toggle id is the same in both states, and the heading.
  - `e2e/tests/catalog-item-modifiers-focus-2727.spec.ts` has three
    keyboard-only tests, including slow-response cases.

No new strings, CSS or help text. The visible change is the focus ring, so
the manual doesn't change.

## Findings (Opus review)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Medium | The fallback used a page-wide flag that any htmx settle could clear. On this page the status-bar polls settle every 5 s. A poll landing during a slow detach (for example on an additional till, where detach goes via the main till) cleared the flag, and focus still dropped to `<body>`. Reproduced in a browser. | **Fixed:** the fallback is keyed on the mutation's own `xhr` (beforeRequest → afterRequest/afterSettle). e2e "slow detach" covers it: it fails on the old script (`Received: ""`) and passes now. |
| 2 | Low-Medium | `!panel.contains(activeElement)` took focus back from a user who had moved to Close or Save during the request. | **Fixed:** the fallback acts only when focus is lost (`body`, null or disconnected). The same e2e checks focus stays on Close; it failed on the old script (`Received: "item-modifiers-heading"`). |
| 3 | Low | A tab load settling mid-mutation could trigger the fallback. | **Fixed** by the same xhr keying. |
| 4 | Low | The e2e only covered fast responses. In attach-all, the expected value depends on leftover demo groups. | The slow-response test was added. The attach-all branch is accepted: the strict heading path is covered by the detach steps. |

Checked and fine:
- No duplicate ids: the partial renders once, and direct and inherited ids
  use different prefixes.
- html/template escapes the ids.
- Cancelling `hx-confirm` never sets the state.
- `detail.target` is the resolved list.
- Listeners are not duplicated, because the page isn't boosted.

## Verified

- TDD:
  - Reverting `web/ui` makes the Go 2727 tests and the e2e fail. Restoring
    it makes them pass. The reviewer re-ran this independently.
  - Reverting only `catalog.html` makes the heading checks fail, which
    shows the fallback is what those checks test.
- `go build ./...`, `go vet`, `gofmt -l .` and `go test ./...` all pass.
- These guards pass: i18n, i18n-meta, rtl-logical-css, no-showmodal,
  help-topics, data-access, kiosk-engine.
- e2e passes: `catalog-item-modifiers-focus-2727`,
  `catalog-item-modifiers-tab-2211` and
  `catalog-item-editor-attach-only-2330`, 9 tests.
- Screenshots after a keyboard detach at 1024×600 and 360 px, looked at: the
  focus outline on the "Modifiers" heading is clearly visible.
- Not checked: dark theme, RTL and German. There is no visual change beyond
  the native focus ring. Not tried on a real touch or switch device.

## Verdict

Safe to merge.
