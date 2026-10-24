# Review — UI scale bounded by window width (ut-docs#4625)

**Date:** 2026-10-24 · **Lane:** lane:cloud-41 · **Built by:** Sonnet 5.5 ·
**Reviewed by:** Opus 5.5 (independent subagent, revert check in its own worktree).
The review fixes were made by the Opus orchestrator.

## What shipped
- **`web/public/app.css`:** the root font-size is now
  `min(ui_scale × --fluid-fs, max(--fluid-fs, --ui-scale-min-width))`.
  - `--ui-scale-min-width` is `100vw / 28.3` up to 900px. That is the stacked tier's narrowest width at scale 1 (481px / 17px).
  - Above 900px it is `100vw / 46`. That is the side-by-side grid: rail, basket, divider and products, measured at 45.8rem.
  - So a manual scale never makes a screen narrower, in rem, than a layout we already design for.
  - Scale ≤ 1 is never touched. A scale the window has room for is kept in full: 1920 wide at 2 is still 40px.
  - Cause: ui_scale multiplies rem (ADR-0008), but the breakpoints are px.
  - The stale comment about the kiosk pages' inline font-size is gone. Those pages set `--ui-scale` now and get the bound too.
- **`e2e/tests/portrait-no-clip-sweep-3060.spec.ts`:** a new ui_scale 2 test at 540×720, 600×960, 768×1024, 800×1280, 1024×600, 1280×800, 1440×900 and 1920×1080. It asserts:
  - the expected root font-size (computed from the same curve);
  - no sideways page scroll;
  - the 3060 sweep on the tender panel and on the products panel (clip, viewport, hit-test);
  - every scan-row and action-row control inside the tender card;
  - Pay's full label at 900px and below.
- **`web/help/{en,de,tr,ar,fa}/display.md`:** one sentence in step 3. When the window is too narrow for the chosen scale, the till limits the scale, so it can look smaller there.
- **`web/help/img/manifest.json`:** surface and topic hashes only.
  - `make docs-shots` produced byte-identical PNGs with and without this app.css change. They also differ from the committed set because of this sandbox's rendering.
  - So the committed PNGs are kept, and only the manifest is regenerated.

## Findings
| Severity | Finding | Outcome |
|---|---|---|
| Major | Side by side, scale 2 overflowed sideways at every window from 1000 to ~1550px wide (1024×600: scrollWidth 1556, +532px; 1280×800 +354; 1440×900 +243). It predates this change, and the first draft's comment claimed wide screens were fine. | Fixed. A second bound applies above 900px (`100vw / 46`), and the test covers 1024 to 1920. |
| Minor | The test did not prove the scale is still applied: a rule that dropped `--ui-scale` would have passed. | Fixed. The root font-size is asserted per viewport, including that 1920×1080 stays at 40px. |
| Minor | Existing "ui_scale 2" narrow tests now run at a lower effective scale. Examples: settings-fee-row-251 at 390px is now 17px; the 3060 basket test at 540 is ~19px. The scale-2 workarounds at 540–600px in app.css (the <24rem basket tier and others) now engage less often. | Accepted. They still guard the same layouts at scales the window allows, and they are harmless. Noted here rather than renamed. |
| Nit | In a plugin page frame, `100vw` is the iframe's width, so its content can be bounded more tightly than the chrome around it. | Accepted. `--fluid-fs` was already iframe-relative. |
| Nit | `100vw` includes a classic scrollbar, so the bound is ~0.5px looser than intended. | Accepted. Harmless. |
| Nit | Help wording: "largest size that still fits" over-promised; de said "Kassensystem" and ar said "النظام" against each file's own terms; fa was definite where en says "can". | Fixed. All five reworded, using each file's own term for the till. |
| Nit | An unchecked `/api/pos/reset` and an awkward line loop in the test. | Fixed. |

**Out of scope, filed:** ut-docs#4897. Side by side at scale 1.5 to 2, Pay ellipsizes to "Pa…" (24–40px short), even at 1920 where nothing is capped. The cause is the action row's own layout (Pay plus three icons in the products column), not the window bound. This test checks Pay's label only at 900px and below.

## Verified
- **Revert check:** run twice, once by the reviewer on the first draft and once by the orchestrator on the final test.
  - With main's app.css, the new test fails.
  - 540×720 shows all of these: the "more" category tab clipped by the tab bar, Pay clipped by 162px, the five scan-row controls and the four action-row controls outside the tender card, and a 34px root where 19.08px is expected.
  - 600, 768 and 800 clip Pay by 162, 66 and 34px.
  - 1024 to 1440 scroll sideways by 532, 354 and 243px.
  - With the fix, it passes.
- **Neighbour specs** with the fix: ui-scale-basket, basket-no-horizontal-scroll-391, portrait-tablet-sale-3050, tender-panel-reachable, sale-screen-213, settings-fee-row-251, self-order-tracking-viewport-scale-1175, pos-divider-resize-2308, page-snapshot-no-jump-2496, settings-osk and portrait-no-clip-sweep-3060: 91 passed. The one failure in that run was the new test's side-by-side Pay check, which became #4897. login.spec.ts (auth project, root font-size cases): 16 passed. That was the Dev's run on the first draft; those cases run at scale 1, which neither bound touches.
- **Looked at** (screenshots, ui_scale 2):
  - 540×720 and 600×960 in English: scan row whole, "Pay £1.20" in full, status bar on one line.
  - 540×720 in Persian (RTL): mirrored correctly, nothing clipped.
  - Scale 1: the root size is unchanged at 1024, 1280 and 1920 (17, 17.86 and 20px).
- **Gate:** gofmt clean, `go build ./...`, `go test ./...`, and every `ci.yml` build-job guard runnable here, including docs-shots, rtl-logical-css, help-drift/topics/breadcrumbs, compliance-claims and competitor-naming. A first `internal/plugins` run timed out at 600s while Playwright was saturating the CPU; it was re-run on its own.
- **Not verified:**
  - real touch hardware or the desktop shells (WebKitGTK and WebView2): Chromium emulation only. `min()`/`max()` with custom properties is supported in both shells' engines;
  - German rendered with the de pack (CI has none, #4629).

**Verdict:** safe to merge.
