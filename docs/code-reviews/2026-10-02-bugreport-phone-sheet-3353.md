# 2026-10-02 — Phone: the bug-report panel is a full-screen sheet (ut-docs#3353)

**Lane:** lane:cloud-54 · **Built by:** Opus 5.5 (inline) · **Reviewed by:** Fable (independent subagent)

## What shipped
- `web/public/app.css`, ≤480px only: `#bugreport-panel` becomes a full-screen sheet from under the 56px top bar (`--phone-top-h`) to the bottom edge. z-index 59 sits below the nav's 60, so ☰ (Lock, exit and status in the drawer) and Back stay tappable over it. The insets are `!important`, so a position dragged on a wider layout (inline left/top, ut-docs#2342) can't move the sheet. Other changes:
  - The head is sticky and is no drag handle.
  - Controls are ≥44px; the note sits on its own line with a full-width Save report under it.
  - The thumbnail ✕ is 44px.
  - The padding clears the safe area and the status row.
  - On the sale screen, a visible status row (offline, main till, power) drops to the bottom edge and rises above the open sheet.
  - The superseded ut-docs#2364 ≤480px offset rule is removed.
  - Tablet, desktop and Pi layouts are unchanged: docs-shots PNGs are byte-identical, and a 1024×600 screenshot was looked at.
- `web/ui/partials/bugreport_panel.html`: a `phoneTier()` helper. At ≤480px the head's pointerdown doesn't start a drag, and `reclampIfDragged` (on open and on resize) leaves both the inline position and the stored `pos` alone.
- Manual: `web/help/{en,de,tr,ar,fa}/bug-reporting.md` gained a phone paragraph. `make docs-shots` was re-run and the manifest regenerated.
- New spec `e2e/tests/bugreport-phone-sheet-3353.spec.ts`, at 360×640, 390×844 and 440×956:
  - geometry on `/` and `/settings`: no sideways scroll, ≥44px controls, Send under the note and on screen, ☰ and the open drawer on top;
  - the head doesn't drag, and an old inline position can't move the sheet;
  - `/report-issue` server-open paints under the bar without JS's measured var;
  - a saved `pos` survives open and resize at phone width;
  - sale-screen status row above the sheet;
  - RTL.

## Review findings (Fable)
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | At phone width, `reclampIfDragged` (open, rotate, keyboard resize) clamped a saved drag position against the full-screen box and persisted `pos: [0, ≤56]`. Back on a wider layout the card was parked top-left under the rail. | Fixed: a `phoneTier()` early return. Spec added; it fails without the guard (3/3) and passes with it. |
| 2 | minor | On the sale screen the problem-only status row (z 39) was covered by the sheet | Fixed: the row is lifted to the bottom edge at z 60 while the sheet is open. `:has()` id specificity is scoped away from the drawer-foot and scrim lifts. Spec added. |
| 3 | minor | The `var(--topbar-h, 56px)` fallback was dead (`:root` 12rem), so a server-opened `/report-issue` first painted 192px down | Fixed: uses `--phone-top-h`. Spec added. |
| 4 | minor | Thumbnail ✕ was 36px | Fixed: 2.75rem (44px) |
| 5 | nit | The old ≤480 offset rule was always out-ranked | Removed |
| 6 | nit | de "Feld" vs "Panel"; fa label spelling vs fa.json; tr labels ran together | Fixed |
| 7 | nit | RTL case sets `dir` after load instead of loading an RTL locale | Accepted: the geometry check is valid, and the drawer and sheet don't depend on load-time dir |

The reviewer also checked and cleared:
- z-order against the toast, the OSK and the ADR-0122 closing rule;
- logical properties only, and every change gated inside ≤480px or the matching matchMedia;
- iOS keyboard (note and Send sit in the top ~300px);
- sticky head with negative inline margins;
- the `touch-action` override.

## Verified beyond automated tests
- **TDD:** the new spec run against `origin/main`'s CSS/JS failed 12/12; on the branch it passes, 21/21 after the review additions. Finding 1's test failed 3/3 with only the guard removed.
- **Screenshots read by eye:** 390×844 sale screen with the sheet open and scrolled, the ☰ drawer over the sheet, and 1024×600 with the panel open (unchanged floating card).
- **Gate:**
  - `gofmt`, `go build ./...`, `go test ./...` all clean.
  - Every `ci.yml` build guard is green except two environment-only failures that reproduce on `main`: `guard-deadcode-baseline` (no GTK headers here, so `cmd/unitill-desktop` can't be analysed) and `guard-shellcheck-version` (no shellcheck binary).
  - e2e: panel, persistence, popup-fit, phone-sell, phone sweep, width-413, popup-zoom and persistent-shell specs: 182 passed and 1 failed. The failure is `/backoffice` in the #3297 sweep under parallel load. It passed alone, both on the branch and with `main`'s CSS, and the panel is closed on that route.

## Not verified
- A real iPhone, Safari or WKWebView: this was Chromium mobile emulation only.
- Real touch-swipe scrolling of the sheet: emulated only.
- A screen reader.

## Verdict
Safe to merge.
