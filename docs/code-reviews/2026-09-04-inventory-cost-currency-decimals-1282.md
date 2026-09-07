# 2026-09-04 — inventory cost currency-decimals fix (ut-docs#1282)

## What shipped

ut-docs#1282 originally named three money-conversion sites hardcoding
`* 100` regardless of the shop's configured currency (wrong by 100x on any
0-decimal currency — IRR/IRT/IQD/AFN/JPY). At pick-up, sanity-checking the
card against recent history found **two of the three already fixed**:
`internal/pages/promotions_page.go`'s `parsePromotionForm` and
`internal/pages/settings_page.go`'s `/api/settings/payments-fee` handler
were both fixed by ut-docs#1400 (merged 2026-09-02, PR universal-till#719,
`httpx.MinorFromMajor(major, httpx.ActiveCurrency().Decimals)`). Confirmed
via `git log` and reading both files on `main` before touching anything.

**Only the third site was still broken and is what this diff fixes**:
`web/ui/pages/inventory.html`'s `#stock-form` submit handler converted the
operator-entered cost (major units) to the hidden `cost_price` (minor
units) via `Math.round(pounds * 100)`. Fix: delegate to the existing
`window.utCurrency.toMinor()` helper (`web/public/app.js`), same pattern
already used by `shifts.html`/`menu.html`/`catalog.html`'s money fields
(ut-docs#1272/#1274/#1400 precedent).

`cost_price` is optional server-side (`internal/pages/inventory_api.go`
keys on `cpStr != ""`) — a blank cost must still round-trip to an empty
posted value, not `"0"`. `window.utCurrency.toMinor('')` actually returns
the number `0` (not `NaN`), so the blank check happens on the raw text
**before** calling `toMinor()`, not after.

Also replaced the field's hand-rolled
`{{ if eq currency.Decimals 0 }}[0-9]+{{ else }}...{{ end }}` pattern
ternary with the shared `{{ moneypattern currency.Decimals false }}`
helper (ut-docs#1274, `internal/httpx/currency.go`) — same convention as
every sibling money field, so a future 3-decimal currency (KWD/BHD/OMR)
doesn't need this call site updated in lockstep with the others (review
finding, fixed).

Added a belt-and-braces `isNaN` check alongside the blank check: the
field's own `pattern=` already blocks a real user-driven submit from
reaching the handler with anything non-numeric, but `toMinor()` itself
never returns `NaN` (`isNaN(num) ? 0 : ...`), so a non-numeric value
reaching this via a non-native path (e.g. a programmatic `dispatchEvent`
that bypasses constraint validation) would otherwise silently post `"0"`
instead of staying blank (review finding, fixed — low practical
reachability, but one token to close).

## New test

`e2e/tests/inventory-cost-currency-decimals-1282.spec.ts`. The e2e till
only ever runs GBP (2 decimals), where the buggy `* 100` and a correct
decimals-aware conversion compute the identical number — a plain "type
12.34, expect 1234" assertion would pass against the unfixed code too and
prove nothing (caught during this session's own TDD verification before
committing). Instead the test monkey-patches `window.utCurrency.toMinor`
to a marker return value (`424242`) unreachable by any accidental
arithmetic, proving genuine delegation. A second test asserts blank stays
blank.

## Independent review

Reviewed by a fresh Opus subagent (complexity:medium → Opus per the
model-routing rule), isolated in its own git worktree so its TDD
revert/restore never touched the shared checkout. **Verdict: safe to
merge — no blockers.** Two nits (fixed, see above), one informational.

What it verified, independently:

- Re-derived the diff scope from `git diff`/`git show` itself, confirmed
  sites 1 and 2 were genuinely already fixed by ut-docs#1400 (not assumed
  from the card text).
- `gofmt -l .`, `go build ./...`, `go vet ./...`, `go test ./...` — all
  clean (no Go files touched by this diff; ran anyway).
- `guard-i18n.sh`, `guard-docs-shots.sh`, `guard-e2e-fixtures-import.sh`,
  `guard-help-topics.sh`, and the rest of the CI-blocking guard set — all
  green.
- Ran the new e2e spec (2 passed) plus regression suites:
  `inventory-to-till.spec.ts` (1 passed) and, most importantly,
  `osk-decimal-admin-fields-1275.spec.ts` — its test 13 does a **real**
  end-to-end submit (item + location + qty + cost "4.20" via the actual
  submit button) and asserts `#stock-cost-minor === "420"`, independently
  proving `window.utCurrency` is reachable at submit time through the
  genuine path, not just the new spec's synthetic-dispatch path.
- **TDD red→green re-verified independently, twice**: (1) reverted only
  `inventory.html` to pre-fix `main`, re-ran the new spec — test 1 failed
  with `Received: "1234"` vs `Expected: "424242"` (the real pre-fix
  arithmetic vs. the mock's marker), exactly as claimed, then restored
  and confirmed green again. (2) Hand-wrote the *naive* wrong fix
  (`toMinor(raw)` unconditionally, no blank check) and confirmed test 2
  fails with `Received: "0"` vs `Expected: ""` — proving the blank-check
  test has real teeth against a plausible wrong implementation, not just
  against the original bug.
- Adversarial checks: `window.utCurrency` availability at submit time
  (traced `base.html`'s `defer`-loaded `app.js` + `body.dataset`, and the
  1275 real-submit test independently confirms it); repo-wide grep for
  every other reader of `#stock-cost`/`#stock-cost-minor` (none missed —
  no reset/clear handler, no override-form overlap); `.trim()`'s behavior
  delta (load-bearing, not cosmetic — without it a whitespace-only value
  would post `"0"`); the `pattern`/`toMinor()` contract stays consistent
  for every currency this product currently ships (0 or 2 decimals).
- No `os.MkdirAll`/`paths.Data` concern (zero Go files in the diff,
  confirmed from the diffstat, not assumed).
- No real client/shop name, no secret-shaped literal.
- UX-guidelines checklist: every item N/A — no markup/CSS/token/layout/
  string/touch-target/RTL change, only the hidden field's arithmetic.
- Help manual: read `web/help/en/inventory.md` in full — no mention of
  cost/currency/minor units at all, nothing it claims is affected. No
  update needed. Independently corroborated by `en/inventory.png`
  regenerating byte-identical (absent from the diff) on this second
  `make docs-shots` run.

## Verified beyond automated tests

Real end-to-end submit through the actual UI (not a mocked API call),
covered by `osk-decimal-admin-fields-1275.spec.ts`'s existing test 13,
re-run against this diff and passing. No new visible UI surface — the
operator-facing `#stock-cost` input's appearance and behavior are
unchanged; only the hidden posted value's correctness changed.

## Explicitly deferred / out of scope

- Four sibling `web/ui/**` sites still carry the same hand-rolled
  decimals-ternary debt this diff closed on `inventory.html`
  (`menu.html`, `promotions.html`, `settings.html`) — not touched here,
  each already uses `moneypattern` in most but not all of their money
  fields per a spot-check; a small follow-up sweep card is the cleaner
  way to close the rest, not bundled into this one-file fix.
