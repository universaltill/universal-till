# Review: the two short side-by-side tender corners #5221 left (ut-docs#5227)

- **Branch:** `fix/5227-tender-corners`
- **Files:** `web/public/app.css`, `e2e/tests/landscape-phone-sale-2918.spec.ts`,
  `web/help/img/manifest.json` (surface hash only)
- **Author:** Opus (build cycle `lane:cloud-41`), inline, from live measurements.
- **Reviewer:** Sonnet, a different model from the author, in a fresh context. It got the diff and the card, not the author's conclusions.

## Change
1. **888-900px wide, <=540px tall.** The basket column takes its 22rem floor
   here, as it already does at 768-844px in the same tier (374px), and gives
   about 72px to the products and tender column. The scan row and the action
   row each stay one line, so the tender keeps its two rows (126.92px).
   Barcode field widths:

   | window | OSK + camera, before | after | + identify, before | after |
   |---|---|---|---|---|
   | 888 | 54.7px | 127.0px | 20.7px | 73.4px |
   | 900 | 66.7px | 139.0px | 20.7px | 85.4px |

   The placeholder needs 87.7px.
2. **768-887px wide, <=540px tall.** Pay is no longer forced onto a line of
   its own (`flex: 1 0 100%` → `flex: 999 1 7rem`). Its 7rem basis never fits
   beside the three to five icons of row 2, so it still gets a full row 3.
   With six icons (plugin identify, OSK, camera, Hold, New sale, Open orders),
   which need 298px against 251px, the sixth joins Pay's row. Before, it opened a
   fourth row (tender 234.44px). The tender now stays at 180.47px, and every icon is at least 44.0px.

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | At 888-900px with a plugin identify button shown as well as the OSK toggle and camera, the field is 73-85px, still under the ~88px placeholder. The comment gave that figure without saying it falls short, and no case covers it. | Comment now says the identify case is still short and why: a third row would fix it but add height. Follow-up card ut-docs#5235. The card's acceptance criterion (OSK shown) is met and tested. |
| 2 | nit | The #5221 comment still said "Pay alone, full width", which the new `flex: 999 1 7rem` contradicts in the six-icon case. | Reworded: "full width (unless an icon spills onto its row)". |
| 3 | nit | "Pay never fits beside 3-5 icons" holds only at UI scale >= 1. Below that, Pay can join three icons, which makes the tender shorter, so no harm. | Comment now says "At UI scale >= 1". |
| 4 | nit | The 888-900px rule ignores a saved `--basket-col-width`. | Accepted: this tier has no divider track. The comment now says so. |

The reviewer also checked these and found them fine:
- no overlapping media ranges, and no theme overrides;
- placeholders in tr, ar and fa are no wider than the English one;
- RTL is direction-agnostic;
- the identify stand-in is valid, with no divergent CSS for `#plugin-identify-open`;
- the `measureText` field check.

The reviewer's own run: `landscape-phone-sale-2918.spec.ts` passed 26 of 26.

## Verification
- **TDD:** all 6 new cases fail on the old CSS:
  - field 54.7/66.7px against the 87.7px needed at 888x414, 900x414 and 900x540;
  - four rows instead of three at 768x400, 768x480 and 887x414.

  On the new CSS, `landscape-phone-sale-2918.spec.ts` and
  `tablet-tier-totals-416.spec.ts` pass 45 of 45, including the #2918 and #5221 cases and 896x414.
- **Identify stand-in:** the shared worker till has no plugin that answers
  `catalog.identify`. The real one needs the WASM seed of
  `plugin-identify-camera-errors-4102.spec.ts`. So the six-icon cases copy
  the camera button under the real `#plugin-identify-open` id, at its real
  place in the scan row. Only its box matters for layout.
- **Screenshots, looked at:**
  - 900x414 with the OSK toggle: two rows, "Barcode" shown whole.
  - 768x400 with six icons: three rows; the sixth icon sits beside Pay; a tile row is whole.
  - The same two windows in `?lang=ar`: mirrored.
- **Unchanged windows:** 1024x600, 360px and the 541-700px blocks are outside both media queries. The help
  screenshots are all at 1024x600, so no pixel changes. Surface hash
  refreshed with `update-docs-shots-surface-hash.sh` (`Docs-Shots-Unchanged: true`).
- **Guards:** every `scripts/ci/guard-*.sh` named in `ci.yml` passes except
  `guard-shellcheck-version.sh`, because this container has no shellcheck
  binary. No shell file changed, and CI runs it.
