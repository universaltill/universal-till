# Review — item editor saves are targeted swaps, never a reload (ut-docs#2905)

Date: 2026-10-05 · Lane: lane:cloud-54 · Author: Opus 5.5 · Reviewer: Fable (independent subagent)

## What shipped

Tests only — no production change. BA found that the owner's ask is already
met in code:

- **Details** save: `catalog.html`'s submit handler sends the form with `htmx.ajax`
  and `swap: 'none'`. The server answers with only the edited card's OOB
  fragment (ut-docs#1363), and the dialog shows "Saved" (ut-docs#917).
  A refusal goes to `#item-form-msg`.
- **Variants** save: every form `hx-post`s `/api/catalog/variant` and
  re-renders only `#catalog-variants`, with a `variant-saved` status line
  (ut-docs#2815).

Nothing pinned this, so a later edit could bring back a page post without
anything failing. This PR adds:

- `TestItemEditorSavesSwapNotReload` (`internal/pages/targeted_swap_2762_test.go`):
  - neither template calls `location.reload()` or `UT.reload(`;
  - the Details submit prevents the default submit and uses `htmx.ajax` swap:none;
  - every `<form>` in `catalog_variants.html` has `hx-post`;
  - every variant form targets `#catalog-variants` and carries `panelItem`.
- `e2e/tests/targeted-swap-item-editor-2905.spec.ts` uses a marker on
  `window` that any reload would wipe, plus a stale attribute that proves
  the region was swapped. It covers four saves:
  - Details success: the card is re-rendered and the dialog says Saved;
  - Details refusal: duplicate SKU, 400, the error shows in the dialog and the dialog stays open;
  - Variants add and edit: only the panel is swapped and it says Saved;
  - Variants refusal: duplicate variant SKU, 400, the panel is not swapped, the input stays as typed and the error shows.

## Verification

- Red/green: a `location.reload()` injected after `item/update` and `variant`
  responses fails both specs; restored, both pass. Turning a variant form into
  a native `action=/method=post` form, or adding `location.reload()` to
  `catalog.html`, fails the Go guard; restored, it passes.
- Gate: gofmt clean, `go build ./...`, `go test ./...` and every
  `ci.yml` build-job guard ran locally.
  - Three `internal/pages` tests failed once because they read `en.json`
    while a guard script was rewriting it in a run beside them. The package
    passed when re-run on its own.
  - `guard-shellcheck-version.sh` needs a shellcheck binary that this
    container doesn't have. CI has it.

## Review findings (Fable)

| Sev | Finding | Outcome |
|---|---|---|
| minor | Console exemption `/\b400\b/` broader than precedent | Fixed — uses `htmx-admin-error-swap-916`'s regex |
| minor | Go guard slices with an unchecked `Index` → panic, not a message | Fixed — `t.Fatalf` on an unterminated form |
| nit | `ev.preventDefault();` anchor matched three unrelated handlers | Fixed — anchored to the item form's submit listener |
| nit | `hx-target`/`hx-swap` adjacency brittle to attribute reorder | Fixed — checked separately |
| nit | `#image-form` and the modifiers partial not in the guard | Accepted — outside the card's "item or variant" scope; image upload already goes through `htmx.ajax` (ut-docs#1363) |

Also checked: no i18n keys and no production code changed. The spec has no
real shop or person names, and the e2e fixtures-import and no-browser guards
pass.

Verdict: safe to merge.

Not changed: the sell-screen tile deep link (`/catalog?item=…&return=/`)
still goes back to the sale screen when the dialog closes. That is
deliberate navigation, not a reload of the editor.
