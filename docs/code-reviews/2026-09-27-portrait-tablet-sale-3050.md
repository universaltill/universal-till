# 2026-09-27 — Sale screen on upright tablets (ut-docs#3050)

## What shipped
- **Basket line by the basket's own width** (`app.css`, CSS only).
  - Between 481px and 900px wide, and at least 481px tall, `.basket` is an inline-size container.
  - Container queries in rem choose the line: ≥ 38rem gives one row (qty, −/+ at 46×46, discount); 24–38rem puts the name on its own row above the controls; below 24rem the stacked tier stays as it was.
  - Container `rem` is the root font size, so `--ui-scale` is covered.
- **Basket cap 38dvh** (was 45dvh) on stacked tablets at least 900px tall. The products row takes the freed height.
- **Status bar wraps** instead of overflowing the page (found by the full e2e suite: 600px wide with the Update chip showing gave a 712px page).
- **UX record** `docs/ux/portrait-and-phone.md`: upright tablets as built, the phone design for ut-docs#3059, and ut-docs#1985's three decisions.
- Measured at 800×1280 with 2 lines: lines 119 → 58px; products 600 → 731px (47% → 57%).

## Review
Independent reviewer: Fable 5.1. The change was built by Opus 5.5.
- First pass verdict: not safe as-is (items 1–5).
- A fresh second pass on the fixes gave **safe to merge**, with items 6–8. It ran 93 specs green, including page-transitions-2223, popup-scrim-2873 and record-dialog-2122, and did a live probe on Chromium 149.

TDD re-verified by the reviewer: reverting `app.css` makes the spec fail (`basket line height`, 119px against ≤ 64 and ≤ 110). It passes restored. The status-bar case was re-verified here: without `flex-wrap`, the page measured 856px wide at 600px.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | Kiosk: `body.kiosk .qty-input {min-height:2.1rem}` beat the 46px floor, so inputs were 36px beside 46px steppers. | **Fixed:** a kiosk selector with one more class. There is a spec case. |
| 2 | should-fix | At ui_scale 1.5–2, the viewport-query one-row line overflowed: name 0px at 800px scale 1.5; remove button at x=940 at scale 2. | **Fixed:** the line layout is now a container query in rem, and a scaled till falls back by itself. Spec cases at scale 1.25 and 1.5. At scale 2 the stacked tier's pre-existing overflow is unchanged (noted on ut-docs#3060). |
| 3 | should-fix | `(orientation: portrait)` flipped the layout when the Android keyboard shrank the viewport. | **Fixed:** gated on width and `min-height`. Spec case: 800×1280 → 800×760 keeps the line. |
| 4 | nit | The 2-row tier missed the last-row border reset. | Fixed. |
| 5 | nit | Silent padding override between the tiers. | Fixed: the padding lives in its own ≥ 38rem query, and the 2-row tier's comment says why it's tighter. |
| 6 | should-fix | Engines from before the 2023 CSSWG change make a container the containing block for `position: fixed` descendants: the #2338 hazard. Chromium 149 measured fine: the shrinkage sheet is centred on the viewport at 800×1280, 900×600 and 600×960. | **Fixed:** new Go guard `TestAppCSSContainerOnlyOnTheBasket`. It fails when any other selector becomes a container, verified by adding one to `.menu-grid`. The Pi 5 WebKitGTK check is part of the device check. |
| 7 | nit | The comment said tier 2 applies at 800px scale 1.5; the measurement shows the stacked tier there (tier 2 is at scale 1.25). | Fixed. |
| 8 | note | Short stacked landscape windows (900×600, 880×640, 768×600) change too: lines 119 → 59px, steppers 27×44 → 46×46. No overflow, Pay visible, #416 green. | Accepted as an improvement; documented in the CSS comment and the UX record. The tier-2 header "Qty" label is narrower than its cell but end-aligned, so it still reads. |

## Verified
- New spec `e2e/tests/portrait-tablet-sale-3050.spec.ts`, 14 cases:
  - 800×1280 and 600×960 × en/fa × light/dark: page doesn't scroll either way; line height; steppers ≥ 46px; name cell ≥ 160px; products' share; Pay in view and hit-testable with 9 lines;
  - kiosk; scale 1.25 and 1.5; keyboard resize; full status bar; landscape no-leak.
- Related specs green: #416, #1314, #1338, #391, #213, #2702, ui-scale-basket (65 passed).
- Full e2e suite:
  - First version: 787 passed. It surfaced the status-bar overflow; the other failure was my port override, `worker-server-isolation`, which expects 9091.
  - After the fixes: 792 passed, 2 failed.
    - `money-comma-2925` timed out under load; it passes alone and passed in the first run.
    - The 600×960 products floor measured 38.9% when the status bar wrapped to 2 rows, so the floor is now 37% with the reason in the spec.
- PR CI caught a font-dependent check: `.line-name` shrinks to fit its text, so "name ≥ 120px" read 118.9px on the Linux runner's fonts. The spec now measures the name cell's available width: 230px at 800, 440px at 600; floor 160px.
- `go build`, `go vet`, and every `ci.yml` build-job guard. Local-only exceptions: shellcheck 0.11 vs the pinned 0.9, and deadcode skipping `cmd/unitill-desktop` without GTK headers. Both are environment-only.
- docs-shots: the manual's screenshots are 1024×600 landscape, which none of these rules reach. The surface hash was refreshed and the guard is green.
- Screenshots looked at: 800×1280 en, 600×960 fa (RTL), 1024×600 and 1280×800 en.
- **Not checked:** real touch hardware. Next step: the TECLAST rotated upright in the Android app, a local-lane device check after merge.

## Deferred
- Phone tier: ut-docs#3059.
- Other screens at these sizes, plus the scale-2 overflow: ut-docs#3060.
