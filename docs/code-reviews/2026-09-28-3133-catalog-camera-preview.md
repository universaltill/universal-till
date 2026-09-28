# Review: catalog photo viewfinder size + captured photo shown and saved (ut-docs#3133)

**Date:** 2026-09-28 · **Lane:** lane:local · **Built by:** Claude Opus 5.5 · **Reviewed by:** Claude Fable 5.1 (independent subagent)

## What shipped
- **Viewfinder size.** `.camera-overlay-video` is `100%` wide at 4:3; since
  the item editor became a full-width dialog (#1956) that was ~1230×920 px
  on the 1280×800 tablet (owner report), top under the tab bar. Scoped
  `#image-viewfinder .camera-overlay-video { inline-size: min(100%, 20rem) }`
  (~270 px tall); AI-identify and barcode previews unchanged. The viewfinder
  scrolls into view on open.
- **Picture on the Item image tab.** `GET /api/catalog/item/icon-state` also
  returns `thumbnail_url` (versioned via new `httpx.ImgVersion`, the `imgv`
  func). New `#image-current` shows the item's current picture, or the photo
  just taken/chosen.
- **Capture saves.** After Capture the photo shows at once and the existing
  upload form is submitted automatically; Choose File previews and still
  saves via Upload Image.
- Manual `catalog.md` (en/de/tr/fa/ar) updated; docs-shots regenerated.

## Findings (Fable)
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | Medium | Upload finishing after the operator switched item painted item A's photo under item B (late/out-of-order icon-state) | **Fixed**: responses for an item no longer selected are dropped; e2e with a delayed upload |
| 2 | Low-med | Chosen-but-unsaved file followed the operator to the next item (Upload Image would save it there) | **Fixed**: cleared on item switch/reset; e2e |
| 3 | Low | Failed icon-state read left the previous picture | **Fixed**: preview cleared on failure |
| 4 | Low | After auto-upload the file stayed armed for a duplicate upload | **Fixed**: cleared after a successful save; e2e |
| 5 | Info | `toBeInViewport` proves on-screen, not un-occluded | Accepted: the dialog head isn't sticky over the panel body |

Reviewer confirmed: `dispatchEvent(new Event('submit'))` reaches the form's
own listener on every target engine and no listener uses `submitter`; object
URLs revoked; logical properties + existing tokens; manual labels match the
real button text in all 5 locales (de from the pack).

## Verified
- TDD: Go icon-state assertions failed without the handler change; e2e
  sizing failed pre-fix (919.8 px at 1280×800, 729.8 px at 1024×600 — the
  owner's symptom), capture-save and preview tests failed pre-fix; the three
  review-finding tests failed before their fixes.
- `go build`, `go vet`, full `go test ./...` green; guards data-access, i18n,
  help-topics, help-drift, docs-shots green; 98 catalog/image e2e green.
- Looked at screenshots: en, light theme, 1280×800 and 1024×600 (viewfinder
  open; after capture). NOT looked at: RTL, dark theme; real camera (headless
  has no frames) — due on the tablet after release.

**Verdict:** safe to merge.
