# Review: phone item-form head in edit mode + RTL `/items` sideways scroll (ut-docs#6138)

**Change.** Two phone-width layout fixes, CSS only (`web/public/app.css`), with e2e coverage in `e2e/tests/catalog-item-form-2000.spec.ts`.
- **Edit-mode head.** At 360×640 the pinned `.catalog-form-head` took 32.3% of the screen in edit mode (create mode was 28.2%). In edit mode the visible +New button joins Close and Save, so the 12rem title group no longer fit beside the action group and the head wrapped into two button rows. Inside the existing `@media (max-width: 700px)` block, `.catalog-form-head-title` is now `display: contents` and its `h3` is `flex: 1 0 100%`. The title gets a full-width text line of its own, and Delete joins +New, Close and Save on one button row.
- **`/items` sideways scroll in fa/ar.** In the stacked `.items-layout` (`max-width: 52rem`), the single track was `1fr`, which is `minmax(auto, 1fr)`. Its automatic minimum was the min-content of the rail rows' nowrap text, so the page was 1–5px wider than a 360px viewport in fa and ar. The track is now `minmax(0, 1fr)`, and the rows ellipsise (`.items-text`).
- `web/help/img/manifest.json`: `surface_sha256` was refreshed with `scripts/ci/update-docs-shots-surface-hash.sh`, because no docs-shot pixel changes (see R2).

**Pipeline.** Dev was Sonnet 5.5. Reviewer and Tester was Opus 5.5, an independent agent. A first review run was killed by a container restart, and this run re-did the verification from scratch.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| R1 | medium (UX) | Dev's first head fix (`.catalog-form-head-title { flex-basis: 6rem }`) met the < 30% target (23.0%), but it left the edit-mode `h3` 46px wide: "Edit: All-Purpose Cleaner 750ml" showed as "Edi…". The operator could not see which item was open, which fails the UX §7 gate. | **Fixed in review**: `display: contents` on the semantics-free wrapper and `h3 { flex: 1 0 100% }`. A new assertion in the edit-mode test checks that the `h3` is not truncated (`scrollWidth <= clientWidth`) and that Delete, +New, Close and Save share one row. It is red against the `flex-basis: 6rem` variant with "is truncated (263px in 46px)" and green with the fix. |
| R2 | medium (CI) | `web/public/**` is part of the docs-shots surface. Without a refresh, `guard-docs-shots.sh` exits 1, which is a warning on the PR but **red on push to main** (`run-docs-shots-guard.sh`). | **Fixed**: ran `update-docs-shots-surface-hash.sh`, and the guard now exits 0. A full regeneration is not needed: docs-shots run at 1024×600, both rules sit in ≤700px and ≤52rem queries, and the probe below measured identical 1024×600 geometry. **The commit must carry `Docs-Shots-Unchanged: true`.** |
| R3 | low | Between 481 and 700px the title now always has its own line, so the head grows from 147.5px to 180.7px (for example 18.4% → 22.6% at 480×800, and 16.4% → 20.1% at 600/700×900). At 480 the old layout also truncated the edit title (166 of 263px), and the new one shows it in full. All widths stay well under 30%. | Accepted. Narrowing the media query would bring back the truncation around 480px. |
| R4 | info | At 701px, which is outside this change, the edit title is already truncated (161 of 263px), the same as before. | Pre-existing, not in this card's AC. Note for a follow-up if wanted. |
| R5 | info | The new overflow test's failure message lists `#nav-drawer` (off-canvas) among its "offenders". It is noise in the message only, because the assertion is `scrollWidth === clientWidth`. | Accepted. |

**a11y check (`display: contents`).** `.catalog-form-head-title` is a bare `<div>` with no role, aria or landmark (`web/ui/pages/catalog.html:232`), so flattening it removes no semantics. The `h3#item-form-title` keeps its heading role. `#item-form-delete` keeps its `aria-label`/`title` (`catalog.delete`) and its own `:focus-visible` ring. Its wrapper `fieldset[data-main-till-lock]` was already `display: contents` (app.css `.set-lock, fieldset[data-main-till-lock]`), so the main-till lock still disables it. DOM order, and so keyboard order, is unchanged: delete, +New, Close, Save, then the tabs (probe output). The only other `.catalog-form-head` user is `tax_codes.html`, which has no `-title` wrapper and is unaffected.

## Verification
- **Red/green** (Chromium, `--project=default --trace off`):
  - Pre-fix `app.css` (`HEAD~1`): 3 of 9 failed. The edit head measured "32.3% (207.0px of 640)", `/items` fa had "scrollWidth 365 vs clientWidth 360", and `/items` ar had "361 vs 360". Create mode (28.2%) and `/catalog` fa/ar passed before as well.
  - Dev's first fix (`flex-basis: 6rem`): the edit-mode test failed on "Edit: All-Purpose Cleaner 750ml is truncated (263px in 46px)".
  - Final fix: 9 of 9 passed.
- **Geometry probe**, final vs pre-fix, head height with title clientWidth/scrollWidth:

  | Viewport | Mode | Pre-fix | Final |
  |---|---|---|---|
  | 360×640 | create | 180.7px (28.2%) | 180.7px (28.2%) |
  | 360×640 | edit | 207px (32.3%), title 254/263, 2 button rows | 180.7px (28.2%), title 326/326, 1 button row |
  | 1024×600 | create and edit | 143.5px, title 263/263 | identical |
  | 1280×800 | create and edit | 150.5px | identical |
  | 701×900 | create and edit | 193.4px | identical |
- **Related specs**: `catalog-item-form-2000`, `-1956` (delete flow, focus, 1024×600 one-row bar, no overlap or overflow), `catalog-tab-strip-fade-2024`, `catalog-row-oob-1363`, `items-shell-catalog-top-actions-rail-2090`, `items-admin-panel-title-2162`, `items-shell-catalog-import-taxcodes-dialog-2095` and `phone-layout-sweep-3297` all pass: **132 of 132**.
- **Not run**:
  - the WebKit phone lane, because this spec is not phone-named and CI's `webkit-phone` job covers the named ones;
  - `go test ./...` and golangci-lint, because no Go changed;
  - shellcheck, because no `.sh` changed.
- **Commands**:
  - `gofmt -l .` gives no output, and `go build ./...` passes.
  - Guards exit 0: rtl-logical-css, fixed-dates, i18n, e2e-fixtures-import, e2e-no-browser, e2e-no-lan-discovery, e2e-no-update-check, emoji-font, competitor-naming, compliance-claims, mail-addresses, no-showmodal, no-native-confirm, no-inline-handlers and autofill-suppression.
  - guard-docs-shots exits 0 after the R2 refresh, and exited 1 before it.
- **Screenshots looked at**: `fix-fa-edit-360.png`, which shows the full Persian title on its own line and Delete, +New, Close and Save on one row; `fix-en-edit-1024.png`, which shows the kiosk bar unchanged on one row; and `fix-en-edit-360.png`. Not looked at: dark theme, de at 360 with the final CSS, and real touch hardware (emulated only).
- **Housekeeping**: a stop-hook made the WIP commit `f53963778` (author Farsid Mirza) during this review. It includes the spec edit above, and the reviewer did not commit. The temporary `e2e/node_modules` Playwright shim and the probe spec were removed.

Pre-flight: web/help topic n/a (layout only, no prose or step changes; docs-shots unaffected at 1024×600 → surface hash refreshed, `Docs-Shots-Unchanged: true`); reference page n/a; en.json n/a (no strings); README n/a; follow-up: R4 (701px edit-title truncation) optional card.

**Verdict:** approve with the R1 and R2 fixes. Squash the WIP commit with a *why* subject plus the `Docs-Shots-Unchanged: true` trailer.
