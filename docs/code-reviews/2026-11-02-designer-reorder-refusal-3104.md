# Review: Designer shows a refused category move on the moved row (ut-docs#3104)

Date: 2026-11-02 · Branch `fix/3104-designer-reorder-refusal-in-row` · Lane `lane:cloud-41`
Built by Opus inline (easy card); independent review by Sonnet in a fresh context.

## What shipped

On /designer, a refused category reorder (drag, Alt+↑/↓ or Move
earlier/later) showed its reason in `#designer-categories-msg` at the top of
the Categories section. Once the list was scrolled, that box was above the
viewport, so the operator never saw the reason, and the line it added
pushed every row down.

- `web/public/list-reorder.js` (shared by /designer and /categories) has a
  new optional `[data-reorder-item-msg]` slot inside an item. It remembers
  the item moved last (`subject`). A refusal writes its reason into the last
  such slot whose container is rendered. Without one, it falls back to the
  list's `data-reorder-msg` box as before, which is how /categories still
  works. One failure shows at a time. A later successful save clears the
  failure only while the slot still holds the failure span
  (`[data-reorder-failure]`). The row slot is also the `hx-target` of that
  row's deactivate and edit forms, so their replies are never wiped.
- `web/ui/partials/buttons.html`: each Designer row has two slots:
  - `#designer-cat-msg-<id>`, the existing slot under the name. It is used
    when the row is collapsed (drag, Alt+Arrow).
  - `#designer-cat-move-msg-<id>`, a new slot under the Move earlier/later
    buttons inside the row's form. It is used while the form is open. On a
    360 px phone the form is taller than the space left on screen, so the
    slot under the name would be off-screen.

  Both slots are `role="status" aria-live="polite"`.
- Manual: `web/help/en/till-designer.md` step 6 now has one sentence on
  where the reason shows. `make docs-shots` was run: only `manifest.json`
  changed and no PNG changed, because the new slots are empty and hidden by
  `.designer-cat-msg:empty`.
- No new locale keys. The text is the existing `list_reorder.failed` plus
  the server's localized reason.

## Tests (TDD verified by hand)

- `internal/pages/designer_page_test.go` (`TestButtonsPartial_EditMode`)
  asserts both slots carry `data-reorder-item-msg`. It **fails** with the
  pre-fix template and passes with the fix.
- `e2e/tests/designer-drop-no-reload-3074.spec.ts`, the refusal case:
  - Move later in the open form: the reason is in
    `#designer-cat-move-msg-<a>`, in the viewport, `aria-live=polite`.
  - The section-top box and the name slot stay empty.
  - The on-screen top of every row up to and including the moved row is
    unchanged, along with all the existing checks: same root, same scroll,
    no refetch, strip untouched.
  - The case then closes the form and presses Alt+ArrowDown on the row
    body: the reason moves to `#designer-cat-msg-<a>` and the old one is
    cleared.

  With the pre-fix JS and template the case **failed** (received `""`).
  With the fix, all 5 cases in the spec pass.
- Regression: `category-list-drag-2699.spec.ts` (/categories, no item slots)
  passes.

## Looked at (UX gate)

The refusal was screenshotted with a 409 mocked on 12 seeded `test-3104`
categories, and each shot was looked at:

- 1024×600 and 360×740.
- en and fa (RTL).
- Light and dark.

At 360 px the first build put the reason off-screen, above the tall form.
That is why the second slot under the Move buttons was added. In the final
shots the reason sits directly under the pressed button at both sizes. In
fa it is start-aligned (right) and wraps cleanly.

Not checked:

- A real finger on the pilot till. Playwright gestures are a synthetic
  mouse.
- German, because the CI till has no de pack.
- A live screen reader announcement.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | low | `subject` is read when the queued save starts, not at move time; the code did not say so | Fixed: comment added in `persist()` |
| 2 | low | Rows *below* the moved row still shift down one message line | Accepted. The AC is about the row the operator is looking at: it and every row above stay put, and the reason is in view. Avoiding any shift would need an overlay or toast, which the till does not have. |
| — | info | An `:empty` (display:none) live region that becomes visible may not be announced by every screen reader | Pre-existing pattern for the row's name slot. Not introduced here. |

The reviewer's own `go build/vet/test` run did not finish in its time box.
The author ran the full gate instead: `gofmt`, `go build ./...`,
`go test ./...`, `golangci-lint`, the build-job guards, docs-shots, and
help-drift/topics.

## Verdict

Safe to merge.
