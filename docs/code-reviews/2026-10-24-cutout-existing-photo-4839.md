# Review: "Remove background" on an existing item/category photo (ut-docs#4839)

Date: 2026-10-24 · Lane: `lane:cloud-41` · Built by Sonnet (Dev+Tester subagent),
reviewed by Opus (independent subagent, own worktree), fixes by the orchestrator.

## What shipped

- `web/public/photo-cutout.js`: `utPhotoCutout.offerExisting(input, url)` fetches
  the record's saved photo (same-origin, PNG/JPEG only) into the page's file
  input and runs the existing offer flow (same engines, backgrounds, server
  checks and save path). Use original photo on such a session uploads
  nothing (input emptied, `photo-cutout:accept` with `file: null,
  existing: true`); Retake empties the input and fires `photo-cutout:cleared`.
  Any failure shows the panel's translated "unavailable" message; nothing is
  thrown at the page.
- `web/ui/pages/catalog.html` (Item image tab) and `web/ui/pages/categories.html`
  (category dialog): a **Remove background** button next to the current
  uploaded photo, rendered only for `catalog_management` holders (`AICutout`),
  shown only for a custom photo with nothing pending — never for a built-in
  icon. Reuses `catalog.image.remove_background`; no new i18n keys.
- `web/public/app.css`: two logical-property rules (spacing; category line wraps at 360 px).
- Manual: `web/help/{en,de,ar,fa,tr}/{catalog,categories}.md` describe the
  saved-photo flow; `web/help/img/manifest.json` refreshed.
- Tests: `internal/pages/ai_cutout_existing_4839_test.go` (button rendered for
  managers, absent for cashiers); five e2e tests in
  `e2e/tests/photo-cutout-3127.spec.ts` (cutout saved, Use original uploads
  nothing, late fetch after an item switch dropped, category flow, built-in
  icon gets no button).

Out of scope, filed: wide background palette shared with my. → ut-docs#4980;
my. flow → ut-docs#3130 (existing card).

## Findings (Opus review)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Medium | A fetch still in flight when the operator switched item / reopened the dialog landed in the next record's input; Use this then saved item A's photo onto item B (reproduced). | **Fixed**: per-panel generation bumped by every `reset()` (item switch, dialog reopen, new pick); a superseded fetch is dropped, error message included. New e2e test holds the photo request with `page.route`, switches item, releases: it **failed on the pre-fix JS** (panel visible on item B) and passes now. |
| 2 | Low | After Retake (camera cancelled) the page's button stayed hidden. | **Fixed**: `photo-cutout:cleared` event; both pages repaint on it; e2e asserts the button is back. |
| 3 | Low | Double tap started duplicate fetches. | **Fixed**: the button hides on tap. |
| 4 | Low | Categories help said removal needs setup and linked a non-existent section (pre-existing wording the new sentence inherited). | **Fixed** in all five locales; now "works out of the box", linking *How background removal works*. |
| — | Note | The "existing photo" is the stored thumbnail (≤1600 px edge), not the original upload. | Accepted: that is the only copy the till keeps. |

## Verification

- TDD: Go tests failed with the template buttons removed and passed with them
  (re-verified by the reviewer in its own worktree); the four Dev e2e tests
  failed before the implementation; the race test failed on the pre-fix JS.
- e2e `cutout` project (`run-till-cutout.sh`, fake rembg): 13 passed, 1 skipped
  (env-gated screenshot test). Dev also ran `photo-cutout-builtin-4137` +
  `categories-record-dialog-2010` on the default project: 23 passed.
- `gofmt`, `go build ./...`, `go vet ./internal/pages/...`, `go test ./...`
  (100 packages ok; under full-suite load in this container one timing flake in
  `internal/cloudsync` `TestStockUploadLoopDebouncesOnTheRealtimeTier` — passes
  5/5 alone — and `internal/plugins` hit the 10-min default timeout; neither
  package is touched here, CI's test-heavy job runs them), guards: i18n,
  rtl-logical-css, no-showmodal, help-drift/topics/breadcrumbs, compliance,
  competitor naming, mail addresses, docs-shots.
- Looked at (Dev, Playwright screenshots): item Image tab and category dialog
  with the button, and the open panel — light theme, en and fa (RTL), at
  1024×600 and 360×780. **Not** looked at: dark theme, German/ar/tr layouts,
  a real touchscreen, a real rembg model.

## Verdict

Safe to merge after the fixes above.
