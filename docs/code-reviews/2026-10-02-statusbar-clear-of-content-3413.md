# Review: status bar clear of page content — regression spec (ut-docs#3413)

- **Branch**: `test/3413-statusbar-clear-of-content`
- **Author**: Opus 5.5 (pipeline lane `lane:cloud-54`)
- **Reviewer**: Fable, as an independent subagent with a fresh context. The card is `complexity:medium`, and the reviewer was a different model from the author.
- **Date**: 2026-10-02

## Outcome: the reported bug does not reproduce, so only a test ships

The card reported that the sticky `.statusbar` covers the last control on tall admin pages at narrow widths, once the page is scrolled to the end.

On `main` it doesn't:
- On admin pages, `.statusbar` is `position: sticky; bottom: 0` and the last in-flow element of `#ut-page`.
- At maximum scroll it therefore sits in its own row directly below `<main>`. Measured: `main.bottom == bar.top` on every route.
- The report's probe, `elementFromPoint(innerWidth/2, innerHeight - 10)`, lands inside the bar's own row. That is why it returned the bar.
- Only the phone sale screen (`body.sale-screen`, ≤480px) takes the bar out of flow, and it reserves the bar's measured height itself.

A sweep of all 22 menu routes at 360×740 and 1024×600 found no covered control. Its only hits were controls inside a closed `<details>`, which is a probe false positive. The reviewer reached the same conclusion independently.

## What shipped

`e2e/tests/statusbar-clear-of-content-3413.spec.ts` (default project, 20 tests):
- **Routes:** `/items`, `/catalog`, `/catalog/option-sets`, `/inventory`, `/settings`, `/reports`, `/users/permissions`, `/translations`, `/audit` and `/menu`.
- **Viewports:** 360×740 (touch) and 1024×600.
- **Method:** every vertical scroller is run to its end. Then `<main>`'s content box must end at or above the bar, and no visible control's bottom edge may hit-test to the bar.
- **Also checked:** an HTTP 200, no redirect, a visible bar, and a clean console.

No CSS change, no locale keys, no help change, because no shop-owner-visible behaviour changed.

## Review findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The `covered` hit-test alone misses most routes under a fixed-bar mutation. `overlap` is the check that carries the spec. | **Fixed:** the comment now says `overlap` is load-bearing and `covered` is diagnostic. |
| 2 | minor | A fixed bar with its height reserved via `main` bottom padding falsely failed, because the check measured `main`'s border box. | **Fixed:** the check now measures `main`'s content box. Verified: fixed + `main{padding-block-end:5rem}` passes 20/20. |
| 3 | minor | The header said "body's last in-flow child", but the bar is inside `#ut-page`. The "/orders polls" note was copied from a sibling spec. | **Fixed** (wording). |
| 4 | minor | `goto` checked neither the status nor the final URL, so a redirect would silently test another page. | **Fixed:** asserts 200 and the pathname. |
| 5 | nit | Few routes are genuinely tall. | **Fixed:** added `/translations` and `/audit`. |
| 6 | nit | The scroller list left out `main` itself. | **Fixed.** |
| 7 | nit | No `watchConsole`. | **Fixed.** |

Accepted consequence of fix 2: a fixed bar with *no* reservation now fails only where the bar is taller than `main`'s 2rem bottom padding. That is always true at 360px, where the bar wraps to 58px. At 1024×600 the bar's single 34px row fits in that padding and no content is covered, so a pass there is truthful. The spec's header says so.

## Verified beyond the automated run

- **TDD-style mutation checks, by the author and the reviewer:**
  - Fixed bar, no reservation: fails (10/20 after fix 2, all the 360px cases; 16/16 before it).
  - Negative-margin mutation: fails 16/16 (reviewer).
  - Fixed bar + `main` padding reservation: passes 20/20.
- **Stability:** the reviewer ran `--repeat-each=3` on the first version, 48/48 with no flakes. The final version passed 20/20.
- **Screenshot looked at:** `/settings` at 360×740, scrolled to the end. The last tile ("Advanced") ends above the two-row status bar.
- **Guards:** `guard-e2e-no-browser.sh`, `guard-e2e-fixtures-import.sh` and `guard-i18n.sh` are clean.
- **Not checked:** real touch hardware (this is a layout geometry check only; no touch behaviour changed), RTL and dark theme (the bar's flow position doesn't depend on either).

## Verdict

Safe to merge (test-only).
