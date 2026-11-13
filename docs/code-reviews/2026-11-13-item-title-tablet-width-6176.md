# Review: item editor edit-mode title untruncated at 701–1023px (ut-docs#6176)

**Change.** CSS only (`web/public/app.css`), with e2e coverage in `e2e/tests/catalog-item-form-2000.spec.ts`.
- New `@media (min-width: 701px) and (max-width: 1023px)` block, just before the existing `<=700px` one: `.catalog-form-head-title { display: contents; }` and `.catalog-form-head-title h3 { flex: 1 0 100%; }`. This is the same remedy #6138 applied at `<=700px`. The title gets its own full-width line, and Delete joins +New, Close and Save on the one button row.
- Cause: from 701px the labelled +New/Close/Save group left the 12rem-basis title group only 161–260px. "Edit: All-Purpose Cleaner 750ml" needs 263px.
- `web/help/img/manifest.json`: `surface_sha256` refreshed with `scripts/ci/update-docs-shots-surface-hash.sh`. Every docs-shot is taken at 1024×600, which is outside the new query, so no pixels change. The commit carries `Docs-Shots-Unchanged: true`.

**Pipeline.** BA, Architect, UX and Dev were done by Sonnet 5.5 in one subagent (easy card). Tester was a separate Sonnet 5.5 subagent. Reviewer was an independent Opus 5.5 subagent.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| R1 | low (test gap) | The 1024×600 case checked only that the title was untruncated and the buttons were on one row. If the query's upper bound slipped to 1024 or higher, the kiosk head would grow from 144 to 177px with every test still green. | **Fixed**: at `>=1024px` the test now also asserts that the title's midline lies inside the button band and that the head is `<=150px`. Red with the bound changed to `max-width: 1024px` ("title must share the button row at 1024px (title mid 23, buttons 43-94)"), green with 1023. |
| R2 | low (trade-off) | At 701–1023px the head is one text line taller in both modes (144 → 177px at 768–1023, and 193 → 227 at 701 where the tab strip already wraps). At 1023×600 that is 29.5%. | Accepted. Scoping the rule to edit mode would make the head jump 33px when a new item is saved, and would break the #6138 both-modes precedent. Capping the query at ~900px would leave mid-length names truncating between 900 and 1023px. |
| R3 | info | Widths of 1024–1280 do not truncate at UI scale 1. At 1024×600 with UI scale 1.25 there is ~20px of slack, so a longer name can still ellipsise there. Very long names (89 chars) still ellipsise at 800px. | Pre-existing, outside the AC. |
| R4 | info | Shared e2e ports caused a few flaky failures while two agents ran e2e at once (an ERR_CONNECTION_REFUSED, plus 1956 "delete confirms" and 2727 "attach-all then detach"). All passed on isolated rerun. | Not related to this change: those tests run at 360px or don't touch the head layout. |

## Verification
- **Red/green**: run by Dev, then re-run independently by the Reviewer and the Tester, each in their own worktree. With only the `app.css` hunk reverted, 3 of the 7 new cases fail: 701×900 (263px in 161px), 768×1024 (263 in 228) and 800×900 (263 in 260). With the fix, 7 of 7 pass.
- e2e `catalog-item-form-2000`, `catalog-item-form-1956` and `catalog-tab-strip-fade-2024` with `--workers=1`: 33 passed. The Tester ran the 20 specs that touch the item form head: 99 passed, and the 2 port-contention failures passed on rerun.
- **Geometry**: title `scrollWidth <= clientWidth` at 701, 768, 800, 900 and 1023 wide. 1024×600 is unchanged (head 144px, title beside Delete). 360×740 is unchanged (head 181px). There is no horizontal page scroll.
- **Visual attestation (Tester)**: screenshots read at en 701×900, 800×900, 1023×600, 1024×600 and 360×740; fa and ar at 701×900 and 800×900; dark theme at 800×900; and a long-name case at 800×900. RTL mirrors correctly, and nothing overlaps or clips. Not viewed: German (the pack isn't loaded in this environment), WebKit, and touch hardware.
- **Guards**, all rc=0: docs-shots, rtl-logical-css, fixed-dates, i18n, help-topics, help-breadcrumbs, help-terms, e2e-fixtures-import, e2e-no-browser, e2e-no-lan-discovery, e2e-no-update-check, competitor-naming, compliance-claims, no-showmodal, no-native-confirm and mail-addresses. No Go changed.

Pre-flight: web/help topic n/a (`web/help/en/catalog.md` names the bar's controls, not their positions); docs-shots n/a (all shots are 1024×600, outside the 701–1023 query; surface hash recomputed, guard green); en.json/meta n/a (no strings); reference/README n/a; review record updated (this file).

**Verdict.** Approve. R1 is fixed and the rest is accepted or pre-existing. Nothing deferred.
