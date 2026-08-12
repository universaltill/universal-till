# Code review: `catimport.ParsePrice` German decimal-comma prices

**Card:** universaltill/ut-docs#586 (p1, bug, complexity: hard, pilot-blocking)
**Branch:** `pipeline/586-catimport-german-decimal-comma`
**Date:** 2026-08-12
**Complexity:** hard — Dev via a Fable subagent (worktree-isolated), Review
via an independent Opus subagent (fresh context, isolated worktree), **two**
review rounds — the first round found a real blocker-class (money) issue,
which earns the second round per the standing model-routing rule; the
second round found one further blocker (regression, not present in round
one's diff) plus one non-blocking fix, both applied here. No third round:
the orchestrator personally re-verified (build/vet/full module test/guards/
mutation tests/adversarial edge cases) rather than spawning a third
subagent, since both rounds' findings were narrow and already fully
diagnosed with exact reproduction commands.

## What shipped

`internal/catimport.ParsePrice` mis-parsed German comma-decimal prices
~100x wrong, silently: it treated `.` as the decimal point unconditionally
and dropped `,` entirely, so `"3,50"` (meant to be €3.50) parsed as 35000
minor units instead of 350. Found during independent review of #581 (SumUp
CSV import), ahead of the German café pilot.

- `internal/catimport/catimport.go`: new unexported `normalizeDecimalComma(s
  string, decimals int) string`, generalizing the last-separator-wins
  heuristic `bkp.go`'s `parseBkpSalesPrice` already proved on the `.bkp`
  path (ut-docs#511). `ParsePrice` now calls it before its existing
  digit/`.`/`-` stripping + `ParseFloat` + round-to-minor-units logic
  (otherwise unchanged). Also new `trailingDigits(s string) int` helper.
- `internal/catimport/bkp.go`: `parseBkpSalesPrice` consolidated from its
  own duplicate inline heuristic down to a thin `return
  ParsePrice(strings.TrimSpace(raw), currencyDecimals)` wrapper — verified
  behavior-preserving (below), not just assumed.
- `internal/catimport/catimport_test.go`: `TestParsePrice` (`ParsePrice`'s
  first direct unit test — previously only exercised indirectly through
  CSV-level tests) and `TestNormalizeDecimalComma`, covering the original
  fix, both review rounds' findings, and the accepted residual ambiguity.

## Design

`normalizeDecimalComma`'s rules, in order:

1. No separator at all → unchanged.
2. Both `,` and `.` present → whichever appears LAST is the decimal point,
   the other stripped as thousands (always unambiguous regardless of digit
   count).
3. Only one separator kind, but it repeats (`"1,234,567"`, `"1.234.567"`) →
   thousands grouping **only if the group after the last separator is a
   clean 3 digits** — otherwise left untouched so downstream `ParseFloat`
   fails loudly (round-2 finding N2, below).
4. Exactly one separator: if the **digit run immediately following it**
   (not the whole remainder of the string — round-2 finding N1, below) is
   exactly 3 digits, it's the accepted ambiguity (`"2,900"` could be
   €2,900.00 thousands-grouped or €2.90 decimal-comma), resolved by
   `decimals`: a zero-decimal currency can never have a genuine fraction,
   so it's always the thousands-grouped reading there; any other currency
   keeps the decimal-comma reading. Any other trailing digit count (1-2 or
   4+) is unambiguously a decimal point (comma) or unchanged (dot, matching
   `ParsePrice`'s pre-existing default).

## Independent review — round 1 (Opus, fresh context, isolated worktree)

Ran build/vet/full `internal/catimport` suite/full-module `go test ./...`/
`guard-data-access.sh`/`guard-i18n.sh` — all green. Independently
re-verified the TDD claim by reverting `normalizeDecimalComma` to a no-op
in its own worktree, confirming the targeted tests failed with the exact
originally-reported wrong values (`ParsePrice("3,50", 2) = 35000`,
`ParsePrice("1.234,50", 2) = 123`), then restoring.

**Verified clean, no findings:** the `bkp.go` consolidation is genuinely
behavior-preserving (round 1 diffed the old inline heuristic against the
new wrapper across 23 hand-picked inputs × 3 decimals values — zero
differences); no filesystem/`MkdirAll`/`paths.Data` bug class (diff writes
nothing to disk); no secrets or real client/shop names; no UI surface, no
manual-topic implication (confirmed from `git diff --name-only`, three
files, all under `internal/catimport/`).

**Finding #1 (HIGH, blocking).** The till ships five zero-decimal
currencies (`internal/httpx/currency.go`: IRR, IRT, IQD, AFN, JPY). A
zero-decimal currency can never have a genuine decimal fraction, but the
original heuristic was currency-blind and always read a lone
comma-with-3-trailing-digits as a decimal point — `ParsePrice("12,000", 0)`
produced 12, not 12000, a silent 1000x under-parse. Aggravating: the
product's own `FormatMoney` renders these currencies comma-grouped
(`"980,000 ریال"`), so comma-grouped is exactly the shape a shop owner
writes. **Fixed**: `normalizeDecimalComma` gained a `decimals` parameter;
the ambiguous-3-digit case now resolves to thousands grouping when
`decimals == 0`.

**Finding #2 (MEDIUM, in scope).** The last-wins rule turned a repeated
comma into multi-dot garbage — `ParsePrice("1,234,567", 0)` started hard-
erroring instead of returning 1234567, a real (if loud, non-silent)
regression against pre-fix `main`. **Fixed**: repeated-separator strings
now strip as thousands grouping.

**Finding #3 (nit, doc only).** The `"2,900"` accepted-ambiguity doc
comment swapped the €2.90/€2,900.00 labels. **Fixed.**

## Independent review — round 2 (Opus, fresh context, isolated worktree,
scoped to the delta between round 1's snapshot and the finding-#1/#2 fix)

Re-ran the full gate (build/vet/full suite/full-module test/guards) —
green. Confirmed finding #1 fixed for bare numeric strings and finding #2
fixed for the tested shapes, and the doc-label nit genuinely corrected (not
re-copied with the same swap). Did a fresh adversarial pass specifically on
the new switch-case logic (branch-order overlap for mixed
multi-separator strings, boundary tail lengths, non-default `decimals`
values) — all correct, no new issue there.

**New finding N1 (HIGH, blocking — a regression against pre-fix `main`,
not merely an unfixed pre-existing gap).** The finding-#1 fix checked
`len(trailing) == 3` against the **whole untrimmed remainder** of the
string after the separator, so it only worked for bare numeric strings.
Any trailing currency symbol or code broke it: `ParsePrice("12,000 IRR",
0)` and `ParsePrice("12,000 ریال", 0)` still produced 12, not 12000 —
reinstating the exact bug finding #1 was meant to eliminate, and precisely
the shape four of the five zero-decimal currencies (IRR/IRT/IQD/AFN, all
`Suffix: true`) actually render as. **Fixed**: the ambiguity check now
looks at the run of digits immediately following the separator (via the
new `trailingDigits` helper), stopping at the first non-digit, instead of
demanding the entire remainder be digits.

**New finding N2 (LOW, in scope — fold into the same fix).** The
finding-#2 fix stripped a repeated separator unconditionally, which
silently converted previously-loud errors into absurd values:
`ParsePrice("12.05.2026", 2)` (a German date leaking into a price column)
went from a clean `unparseable price` error to a silent €12,052,026.00.
**Fixed**: repeated-separator stripping now requires the group after the
*last* separator to be a clean 3 digits (genuine thousands grouping always
looks like this, including Indian-style `"1,23,456"`); anything else is
left untouched so it falls through to `ParseFloat`'s existing
`unparseable price` error.

Mutation-tested by round 2 (forcing the currency-aware guard off) and
independently re-tested by the orchestrator after the N1/N2 fix (forcing
`trailingDigits` to always return 0) — in both cases the targeted tests
failed with exactly the reported wrong/regressed values, then passed clean
on restore.

## Verified beyond automated tests (orchestrator, after round 2)

- Re-ran the full `internal/catimport` suite, the whole-module `go test
  ./...`, `go vet ./...`, `gofmt -l`, and all four `CLAUDE.md`-mandated
  guards (`guard-data-access.sh`, `guard-i18n.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`) personally, after the N1/N2 fix — all green.
- Ran the mutation test personally (not just trusting the subagents'
  reports): reverted `trailingDigits` to a stub, confirmed `TestParsePrice`
  and `TestNormalizeDecimalComma` fail with the exact pre-fix wrong values
  for every zero-decimal-currency and symbol-suffixed case, restored, and
  confirmed green again.
- Additional adversarial spot-checks beyond the two reviews' own coverage:
  mixed multi-separator strings (`"1.234.567,89"`, `"1,234,567.89"`,
  3-decimal-currency variants) still resolve correctly via the
  higher-priority "both present" branch; negative-with-comma and pure
  garbage strings (`",,,"`, `"€1.234,-"`) still error cleanly; the one
  accepted residual edge case (a bare trailing separator with nothing
  after it, e.g. `"3.50,"`) behaves exactly as documented and was
  explicitly not re-litigated in round 2 per its own scoping instruction.

## Deferred / explicitly out of scope

- No locale/currency-setting-based disambiguation — heuristic-only,
  matching `bkp.go`'s existing precedent (BA/Architect decision, not
  reopened by either review round).
- `ParseTaxRateBP` untouched — separate function, separate card history
  (#512).
- The bare-trailing-separator edge case (`"3.50,"` → 350 rather than an
  error) remains an accepted, documented tradeoff inherited from
  `parseBkpSalesPrice`'s original review (ut-docs#511) — real callers
  `TrimSpace` their input first, and this shape is rare enough in genuine
  exports not to warrant its own branch.

## Verdict

**Safe to merge.** Two review rounds, both earned by real blocker-class
(money-correctness) findings, both fully fixed and independently
re-verified — the second round specifically checked that the first
round's fix didn't just move the bug rather than close it, and found that
it partially had (N1); the orchestrator's own final pass re-confirmed the
N1/N2 fix against both rounds' original repro cases plus a fresh set of
adversarial inputs. No UI surface, no manual-topic implication, no
outstanding findings.
