# 2026-09-03 — catalog in-page camera viewfinder (ut-docs#1472)

Card: ut-docs#1472 (split from #1435, follow-up to universal-till#737's
Android screenshot/camera/mic permission plumbing).

## What shipped

`web/ui/pages/catalog.html`'s "Item image" panel: the **Take a Photo**
button now opens a live in-page `getUserMedia` viewfinder as the first
choice on any platform that actually supports it — a `<video>` preview,
**Capture** (draws the current frame to canvas, converts to a JPEG Blob,
feeds it into the existing `#image-file` field via `DataTransfer` — same
canonical upload path the file picker and the OS-camera input already
use) and **Cancel**. Feature-detected (`navigator.mediaDevices &&
.getUserMedia`), never platform-sniffed: a desktop/laptop with a webcam
gets the viewfinder for free; no camera, a rejected permission prompt, or
no `getUserMedia` support at all falls straight back to the pre-existing
OS-camera file input (`#image-file-camera`), byte-for-byte unchanged.

Reuses `.camera-overlay-video` (app.css) — the sizing/aspect-ratio class
already shared by the AI-identify and #548 barcode-scan camera previews —
so the new preview is the third consumer of that pattern, not a fourth
one-off.

New Playwright spec `e2e/tests/catalog-camera-viewfinder-1472.spec.ts`
(11 tests) stubs `getUserMedia` deterministically (same technique as
#548's own spec) since this sandbox's headless Chromium never actually
decodes a `captureStream()` into a `<video>` frame buffer — confirmed
while writing the spec, same class of sandboxed-CI media limitation
already documented for `<img>` load-state assertions (ut-docs#1362); the
video element's `videoWidth`/`videoHeight` are stubbed directly instead,
which still exercises the real draw/toBlob/DataTransfer/upload wiring.
One pre-existing test (`catalog-image-to-till.spec.ts`'s
keyboard-activation check) updated to stub a `getUserMedia` rejection
first, since that's what a real camera-less CI runner does and the
button's behavior is now conditional on it.

Manual updated in the same branch (`web/help/{en,fa,tr,ar}/catalog.md`),
screenshots regenerated (`make docs-shots`) since `catalog.html` is part
of the hashed UI surface.

## Independent review

Opus subagent (`complexity:medium` routing — review stays Opus,
deliberately not the model that wrote the fix), isolated worktree, full
gate re-run (build/vet/test/all CI guards + the touched e2e specs) and
three personal TDD reverts (the `viewfinderBusy` double-open guard, the
`[hidden]`/`.field-pair` CSS-specificity fix, and the
`navigator.mediaDevices` existence guard) — all three reverts produced a
real, meaningful failure and were confirmed to pass again restored.

**First-pass verdict: NOT safe to merge — 2 blockers, 4 should-fix.**
All six fixed in this same cycle before commit (blocker-class findings on
a `complexity:medium` card don't by themselves force a second review
round per the pipeline's own process-depth rule — these were fixed and
re-verified directly, not re-reviewed from scratch):

1. **Blocker — video rendered off-panel at the till's own 1024×600
   resolution.** The `<video>` carried no sizing class (raw intrinsic
   ~300–640px width) inside the 360px-wide sticky `.catalog-form` panel;
   measured live, the Capture button was clipped to `"Ca…"` off the right
   edge of the viewport. Fixed by giving the video the existing
   `.camera-overlay-video` class (`inline-size:100%`, `background:#000`,
   `aspect-ratio:4/3`) and moving it to its own row above the button pair
   instead of sharing a 2-column `.field-pair` grid with them. Re-measured
   live at 1024×600 after the fix: video box fully inside the panel
   (screenshot on file).
2. **Blocker — camera stream leaked if the operator collapsed the "Item
   image" `<details>` panel while the viewfinder was open.** One click
   (the same `<summary>` that opened it) left the track `live` indefinitely
   with no visible UI anywhere pointing at it — a privacy issue on a
   shop-floor POS. Fixed with a `toggle` listener on the containing
   `<details>` that calls `stopViewfinder()` when it closes. New
   regression test: `collapsing the Item image panel while the viewfinder
   is open releases the camera`.
3. **Should-fix — `ai.identify.capture`/`common.cancel` key reuse was
   unsound in Persian**: `ai.identify.capture` and `catalog.take_photo`
   are the *identical* Persian string ("گرفتن عکس"), so a fa operator saw
   two simultaneously-visible buttons reading the same label, one of them
   now a silent no-op. Fixed by adding a dedicated
   `catalog.viewfinder.capture` key (distinct in all four locales) instead
   of reusing `ai.identify.capture`; `common.cancel` reuse was fine as-is
   and kept.
4. **Should-fix — capturing before the first decoded frame silently did
   nothing** (`getUserMedia` resolving is not the same as a frame
   existing; a fast operator could tap into that window). The existing
   AI-identify camera capture (`app.js`) already guards this
   (`if (!w || !h) { setStatus(...); return; }`) and this diff hadn't
   mirrored it. Fixed: same guard, reporting a new
   `catalog.viewfinder.not_ready` message and leaving the viewfinder open
   for a retry instead of vanishing. New regression test: `capturing
   before the video has a real frame reports an error and keeps the
   viewfinder open`.
5. **Should-fix — focus dropped to `<body>`** on Capture/Cancel (both
   `display:none` themselves while still focused). Fixed:
   `stopViewfinder()` now refocuses `#image-camera-btn` when focus was
   inside the viewfinder at close time.
6. **Should-fix — `.field-pair` had no `[hidden]` CSS guard**, unlike
   `.field-checks` right next to it in the same stylesheet, despite the
   fix for finding #1 depending on exactly that class of bug. Added
   `.field-pair[hidden] { display: none; }` next to the existing
   `.field-checks[hidden]` rule so a future edit that puts `hidden`
   directly on a `.field-pair` doesn't silently fail the same way.

Nice-to-haves also taken in this pass: capture now downscales to a
1024px long edge + JPEG 0.85 (same bound as the AI-identify capture path,
`app.js`) instead of a full-resolution PNG — the server's 10MB upload cap
silently truncates rather than erroring, so an oversize capture used to
surface as a misleading "not a valid image"; the capture canvas is reset
to 0×0 after each use instead of retaining the last frame in memory; the
three fallback-path tests now assert a fast, explicit click-counter
before the `waitForEvent('filechooser')` wait, so a regression there fails
with a clear "expected 1, received 0" instead of only a bare 30s timeout;
a code comment's cascade reasoning corrected (cascade *origin* is resolved
before specificity — an author rule beats the UA stylesheet's `[hidden]`
regardless of specificity, not "wins the tie").

Confirmed also: zero Go files touched (the two recurring bug classes this
pipeline watches for — missing `os.MkdirAll`, a cwd-relative path where
`paths.Data(...)` belongs — don't apply); no real client/shop name or
secret-shaped literal anywhere in the diff; all four `catalog.md`
manual translations read correctly and consistently (the "on a phone or
tablet" qualifier correctly dropped now that desktop webcams are covered
too); screenshot regen is a normal full-surface regen (`web/ui/**`
touched), not specific to this feature — all `catalog.png` files spot
checked at 1024×600, correct dimensions, no zero-byte files.

## Verification (after all fixes above)

| Check | Result |
|---|---|
| `gofmt -l .` | empty |
| `go build ./...` | clean |
| `go test ./...` (full repo) | pass |
| `guard-i18n.sh` (1344 keys, +2 over baseline) | pass |
| `guard-compliance-claims.sh` / `guard-help-topics.sh` / `guard-docs-shots.sh` | pass |
| All 31 other CI `build`-job guards | pass |
| `e2e`: `catalog-camera-viewfinder-1472.spec.ts` (11 tests) | pass |
| `e2e`: full `catalog` + `sale-screen-camera-barcode-scan-548` suites (46 tests) | pass |
| `e2e`: full `--project=default` suite (302 tests) | pass |
| `e2e`: full `--project=auth` suite (18 tests) | pass |
| B1 fix visually re-measured live at 1024×600 | video fully inside panel bounds |

## Deferred / follow-up (not this card's scope)

- **Language-pack follow-up (mandatory, same cycle, not a card)**: the two
  new `en.json` keys (`catalog.viewfinder.capture`,
  `catalog.viewfinder.not_ready`) need the same keys added to the external
  `ut-plugin-language-{de,es}` packs — done in this same pipeline cycle
  immediately after this PR, see those repos' own history.
- Screen-reader announcement of the viewfinder opening (no live-region
  "camera preview open" cue beyond the focus move to Capture) — noted by
  review as a nice-to-have, not blocking; left for a future UX pass if the
  product owner wants it, since this diff already meets the card's stated
  acceptance criteria.
