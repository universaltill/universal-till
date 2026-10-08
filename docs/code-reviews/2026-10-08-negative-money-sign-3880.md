# Review — negative money leads with its sign (ut-docs#3880)

**Branch:** `fix/3880-negative-money-sign` · **Card:** ut-docs#3880 ·
**Author:** Opus 5.5 (lane:cloud-41) · **Reviewer:** Fable (independent subagent, own worktree)

## What shipped

- `httpx.formatMoney`: the sign now leads the whole amount, symbol
  included: `-£42.50`, `-¥300`, `-€1.234,56`, `-۱۲٬۳۴۵ ریال`. Before, it
  was `£-42.50` / `¥-300`. CLDR (checked through Node's ICU `Intl`) puts the
  sign first in every locale this product ships (en, de, ja, tr, fa, ar).
  The till's own EOD print already wrote `-£411.10`, so screen and print now
  match.
- New `httpx.FormatMoneyDisplay` for on-screen HTML only. In an RTL locale,
  a negative amount whose symbol comes first is wrapped in LRI…PDI
  (U+2066/U+2069). This draws the sign left of the symbol for every
  currency. Unwrapped, the bidi algorithm drew `-¥۳۰۰` as `۳۰۰¥-` while
  `-KWD …` kept its order, which was the reported fa inconsistency. CLDR's fa
  format makes amounts LTR the same way, with an LRM. Suffix amounts
  (`-۱۲٬۳۴۵ ریال`) are not wrapped: they already read sign-first right to
  left, with the word where a positive amount has it.
- `{{ money }}`, the shift-close message, the EOD "done" line and the
  refund total now use the display variant. Print paths keep
  `FormatMoney`/`FormatMoneyLatin`, which never emit bidi controls (the
  ESC/POS raster has no bidi handling).
- `window.utCurrency.format` (`web/public/app.js`) gets the same rule,
  plus the isolate on `<html dir="rtl">`.
- Unknown-code fallback: `XYZ 1.99` instead of `XYZ  1.99` (one space).
  Go now matches the JS twin (review finding 1).
- `ut-docs` `reference/coding-standards.md` states the display rule
  (separate PR).

## Findings (Fable)

No blockers or majors. The reviewer checked:
- every `{{ money }}` attribute use (`basket.html` `data-label` is copied
  into `textContent`, never parsed);
- the one JS parse-back of rendered money (`app.js` `basketTotal` fallback
  strips everything except digits, separators and `-`);
- that print never goes through `{{ money }}`;
- that positive output is byte-identical for every registry currency.

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The tests pinned the old double space in the unknown-code fallback (`XYZ  1.99`), and JS rendered one space | **Fixed**: trimmed in the word/code branch; tests now expect one space |
| 2 | nit | `eod_api.go` `signed` helper is now redundant; `print_api.go:363`/`eod_api.go:286` `"-"+money(x)` would double the sign if `x` were ever negative | **Accepted**: both are print paths, pre-existing and correct for their non-negative inputs |
| 3 | nit | The e2e spec hardcoded `£` | **Fixed**: the first test is currency-agnostic. The RTL test keeps GBP on purpose, since it needs a prefix symbol |
| 4 | nit | Review record still to write; no help page shows a negative amount | This file. No help change needed |

## Verified beyond unit tests

- TDD: `TestFormatMoney`/`TestFormatMoneyDisplay` were written first and
  failed (undefined `FormatMoneyDisplay`, old `£-` order). The page tests
  `reports_page_test`/`shifts_page_test` failed on the new order until
  updated, so they really pin it.
- `e2e/tests/money-negative-sign-3880.spec.ts` measures glyph x-positions
  in real Chromium on an fa (`dir=rtl`) page. The sign is drawn left of `£`
  for both the JS output and the Go display output. A control shows the
  un-isolated string draws it on the right.
- Looked at screenshots at 1024×600 (fa) and 360×800 (en) of the JS
  and Go outputs, including rial suffix amounts.
- Real product surfaces showing a negative amount (refund total, shift
  variance) were **not** screenshotted with a seeded negative in fa; the
  glyph-position spec covers the same strings.
- `make docs-shots`: 120 shots regenerated, **no PNG changed**. Only the
  manifest's surface hash moved.
- Gate: `gofmt`, `go vet`, `go build`. `go test ./...` passed except two
  packages that failed only during a run under heavy concurrent load:
  `internal/pages` (one panic, passes on a clean full re-run) and
  `internal/plugins` (10m timeout/OOM, re-run separately; the diff does not
  touch it). Build-job guards pass, except `guard-shellcheck-version`
  (no `shellcheck` binary in this container; no shell script changed).

## Deferred

- The plugin view's `FormatMoneyIn` (in-flight universal-till#1748,
  ut-docs#3160) shares `formatMoney`'s body. Rebasing it onto this change
  gives it sign-first. On screen it should go through the display (isolate)
  variant too; noted on ut-docs#3880 for that PR.

**Verdict:** safe to merge.
