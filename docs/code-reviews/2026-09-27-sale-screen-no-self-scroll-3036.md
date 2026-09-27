# Review: sale screen no longer scrolls itself at phone width (ut-docs#3036)

- Branch: `fix/3036-sale-screen-no-self-scroll`
- Author: Opus 5.5 (lane:cloud-54). Reviewer: Fable (independent subagent, read-only).
- Card: universaltill/ut-docs#3036 (complexity:medium).

## What shipped

`phone-width-layout-413.spec.ts` "New Sale is reachable at 360px without
scrolling .pos-container" failed on universal-till#1476 (scrollTop 9/35) and
passed elsewhere. Root cause, reproduced locally at 360x640 under a 4-worker
load: the scan field (`input[name=code]`, `web/ui/pages/index.html`) sits just
below `.pos-container`'s fold, and its `autofocus` scrolled the container to
it — or not, depending on whether layout had settled when the browser ran
autofocus. The New Sale buttons' delayed re-focus scrolled it by 180px too.

- `autofocus` removed; an inline script right after the input focuses it at
  parse time with `focus({ preventScroll: true })` (wedge scanners still get
  a focused field at load).
- The three New Sale buttons' delayed `focus()` now pass `preventScroll`.
- Stale comments about "autofocus" updated (osk.js, index-keyboard-1023,
  settings-osk, index_osk_test.go).

## Tests (TDD)

- `internal/pages/index_osk_test.go`: now asserts no `autofocus` on the scan
  input and a `preventScroll` focus script targeting it before the row's next
  input. Verified failing on `main`'s template, passing with the fix.
- New Playwright tests in `phone-width-layout-413.spec.ts`:
  - records every scroll event on `.pos-container` from the first byte
    (`addInitScript`) and asserts none at load, scan field focused;
  - phone New Sale re-focuses the scan field without scrolling;
  - boosted Menu → Till arrival (same shell boot) focuses it without
    scrolling (added after review).
  On `main` these failed 7/12 under `--repeat-each 4 --workers 4` (35px on
  load, 180px after New Sale); with the fix the affected specs
  (413, index-keyboard-1023, settings-osk) passed 96/96 and 75/75 repeated.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix | Boosted rail arrival now runs the focus script (htmx evaluates swapped scripts); previously htmx's autofocus hit the closed hold dialog's field, so the scan field was likely not focused after Menu → Till. Net positive but untested. | Fixed: boosted-arrival e2e test added. |
| 2 | should-fix | Comments describing the scan field's `autofocus` now lie. | Fixed in all five places. |
| 3 | nit | Go test matched a whitespace-sensitive literal and didn't check the target. | Fixed: checks `<script>` before next input containing `preventScroll` and `input[name=code]`. |
| 4 | info | `document.currentScript.parentNode` safe (parse-time and htmx `evalScript`); pre-64 Chromium WebView ignores `preventScroll` and degrades to the old scroll, no error. | Accepted. |
| 5 | info | Parse-time `focus()` reliable (scripts wait for blocking CSS; focus forces layout). | Accepted. |
| 6 | info | `make docs-shots` in this container re-rendered every PNG (fonts differ from CI, incl. pages this diff doesn't touch). | PNGs not committed; no docs shot captures the phone-width scroll, focus state is unchanged, so the surface hash was refreshed (`Docs-Shots-Unchanged: true`). |

## Gate

`gofmt -l` clean, `go build ./...`, `go test ./...` all pass,
`golangci-lint` 0 issues; guards i18n, autofill, osk-loaded, htmx-loaded,
docs-shots, help-topics, help-drift, kiosk-engine, compliance, competitor,
core-neutral, data-access, e2e-fixtures-import, e2e-no-browser,
page-http-error, emoji-font green; full default e2e project run once after
the last edit.

## Verdict

Safe to merge. No user manual prose depends on the scan field scrolling.
