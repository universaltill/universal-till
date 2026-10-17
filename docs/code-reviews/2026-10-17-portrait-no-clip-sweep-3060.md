# Review — portrait + phone no-clip sweep (ut-docs#3060)

**Date:** 2026-10-17 · **Lane:** lane:cloud-41 · **Built by:** Opus 5.5 ·
**Reviewed by:** Sonnet 5.5. Fable was refused because it was out of usage credits.

## What shipped
- **New spec `e2e/tests/portrait-no-clip-sweep-3060.spec.ts`** (17 tests):
  - **Viewports:** 360×800 and 800×1280, in de/fa × light/dark.
  - **Sale flow:** variant picker, modifier picker, line discount, Hold, the payment overlay (Pay and every Split method), cash tender and receipt.
  - **Pages:** /catalog, /menu, every Settings section, and /reports with every tab.
  - **ui_scale 2:** the stacked basket at 800×1280, 768×1024, 600×960 and 540×720.
  - **Checks:**
    - no sideways page scroll;
    - every control is inside the viewport and is what `elementFromPoint` finds at its centre;
    - no `overflow:hidden` ancestor clips a control;
    - dialogs fit the viewport.
- **`web/public/app.css`** (logical properties only):
  - `.split-grid` uses `minmax(0,1fr)` tracks, and its fields fill them. The voucher row had pushed the fields 49px off a 360px screen.
  - **New basket container tier (<24rem).** Each line becomes a small card, so ui_scale 2 lines no longer scroll sideways (+7/+39/+207/+267px on main) and the ✕ is not clipped.
  - In the same tier, long codes break, and the Dine in / Takeaway toggle stacks its segments.
  - `.statusbar .sb-enrol` (≥481px) may wrap only when it alone is wider than the bar. Before, it scrolled the page sideways by 38–98px at ui_scale 2. The bar stays one row (#3459).
  - **Phone sheet:** reserves `--phone-sb-h` as end padding, and the sticky tender uses `bottom:0`. With a status chip up, the last 44–68px of the sheet had stayed under Pay for good.
- **`web/ui/pages/settings.html`:** the Printer card's action row wraps. "Beleg-Designer" had been clipped by the card in German.
- **`web/help/img/manifest.json`:** surface hash only. `make docs-shots` changed no PNG.

## Findings
| Severity | Finding | Outcome |
|---|---|---|
| Minor | Fixed `waitForTimeout(100/200)` on Settings/Reports section switches. | Accepted. They are short settle waits after a deep-link hash change, which has no event to wait on, and they were stable over repeated runs. |
| Minor | The German checks need the de pack overlay, which CI lacks. | Accepted. The spec records an annotation, and the follow-up is ut-docs#4629. |
| Nit | The ui_scale 2 test sweeps only `#basket`. | Accepted. The rest of the sale screen at 540–600px is filed as ut-docs#4625. |

No blocker or major findings. The reviewer checked:
- the new container tier cannot match at ui_scale 1 on a phone or kiosk, because the basket is a container only at 481–900px;
- the `.sb-enrol` change keeps #3459 green;
- the Pay bar is unchanged with no chip showing.

## Verified
- **Revert check:** run twice, once by the Dev and once independently by the reviewer. With main's app.css and settings.html, the spec fails 9/17 with the de pack and 7/17 without it. With the fixes it passes 17/17.
- **Neighbour specs** with the fixes, all green: 3050, 391, 413, 3459, ui-scale-basket, 3351 and 3413. The Dev ran 47 files in all. The failures in that run were 3059, 4370 and 1960, which also fail on main in this sandbox, and 3050 and 3459, which fail only while the sandbox's disk-low and update chips show; the reviewer re-ran both green.
- **Gates:** `go build`, `go vet` and `gofmt` are clean. `go test ./web/... ./internal/httpx/...` passes. These guards pass: rtl-logical-css, i18n, i18n-meta, no-showmodal, help-topics, help-drift and docs-shots.
- **Screenshots looked at:**
  - Split/Voucher, receipt end, and the Printer card at 360 and 800, in de-light and fa-dark;
  - ui_scale 2 sale screen at 900, 800, 768, 600 and 540, before and after.

## Not verified
- No real touch hardware; emulated only.
- Variant and modifier picker, Hold, Reports, Catalog and Menu were measured geometrically only, not looked at.

## Deferred (filed)
- #4625: the sale screen at ui_scale 2 on a 540–600px window.
- #4626: raw payment-method ids are shown.
- #4627: an over-price line discount is accepted.
- #4628: Settings two-pane is cramped at 800×1280.
- #4629: install the de pack in e2e CI.

**Verdict:** safe to merge.
