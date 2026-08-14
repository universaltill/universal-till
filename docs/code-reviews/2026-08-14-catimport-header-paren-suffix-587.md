# Review: catimport header-synonym matching doesn't strip parenthesised suffixes (ut-docs#587)

**Date**: 2026-08-14
**Card**: universaltill/ut-docs#587 — "catimport: header-synonym matching doesn't strip parenthesised suffixes, so column variants silently miss (e.g. \"Tax rate(%)\", \"VAT rate (%)\")"
**Complexity**: medium
**Reviewer model**: Opus subagent, worktree-isolated (per this card's `complexity:medium` tier — see `scrum-master` skill's model routing), two rounds (see below)

## What shipped

`internal/catimport/catimport.go`'s `headerIndex`/`hasColumn` matched a CSV
header against `columnSynonyms` only via exact-match or a `synonym + " ["`
bracket-prefix rule (`"Current Quantity [Main]"` matching `"quantity"`).
Header variants carrying a trailing parenthesised suffix —
`"Tax rate(%)"` (no space), `"VAT rate (%)"` (not in the tax synonym
list), `"Tax (%)"` — missed their synonym entirely, so the column was
silently unrecognised: no `TaxIssue`, no warning, the row just imports at
the till's default tax rate. Compliance-sensitive (ut-docs#512).

- New `stripTrailingParen(key string) string` helper: strips one trailing
  `"(...)"` (and preceding whitespace) from an already-lowercased header
  key.
- `headerIndex` now runs **two passes**: pass 1 is the original
  exact-match/bracket-prefix rule only, across every header; pass 2 tries
  the paren-stripped key, but only for fields no header claimed in pass 1
  (see finding B1 below for why this had to be two passes, not one).
- `hasColumn` (single-field existence check, no cross-field claiming) gets
  the same paren-stripped fallback in one pass — no shadowing risk there,
  since it isn't choosing between competing fields.
- The now-redundant literal `"tax rate (%)"` entry in
  `columnSynonyms["tax"]` (added by #581 for one exact SumUp string) was
  removed — the general stripping rule subsumes it — and its neighbouring
  comment rewritten.
- New tests: `TestHeaderMatchingParenthesisedSuffix` (table:
  `"Tax rate(%)"`/`"VAT rate (%)"`/`"Tax (%)"` all resolve to `tax`),
  `TestHeaderMatchingParenthesisedSuffixNonTaxField` (`"Price(GBP)"`
  resolves to `price`, proving the fix is general, not tax-specific),
  `TestHeaderMatchingParenSuffixNeverShadowsAnExactMatch` (added during
  review — see below).

TDD-first throughout: every new test was written and confirmed failing
against the code as it stood at that point, then made to pass — twice
over, once for the original fix and once for the B1 fix (both confirmed
independently by the reviewer, not just taken on the implementer's word;
see "Verified beyond automated tests").

## Independent review (Opus, worktree-isolated, two rounds)

**Round 1 — full diff.** Verdict: **NOT SAFE TO MERGE.**

**B1 (blocking) — the paren-stripped fallback could shadow a genuinely
exact match on a different header, silently mis-assigning a column.**
The original single-pass implementation added `stripped == s` as a peer
match condition alongside the exact/bracket rule, evaluated per-header in
header order. So an *earlier* header's lenient match could claim a field
before a *later* header's *exact* match got a turn. Measured, before vs.
after the original (single-pass) diff:

| header row | pre-#587 | post-#587 (single-pass) |
|---|---|---|
| `Name,Price (cost),Price` | price → col 2, **450** ✅ | price → col 1, **110** ❌ (cost price sold as retail) |
| `Name,Price,VAT (takeaway),VAT rate` | taxBP **1900** ✅ | taxBP **700** ❌ (takeaway rate booked as dine-in) |

Exactly the silent money/tax-corruption class this file's own prior
findings (ut-docs#512, #586 — see `normalizeDecimalComma`'s doc comment)
exist to prevent, and the same class this very card exists to fix for
*missing* columns — shipping a fix for one silent-drop shape that opens a
silent-swap shape would be a net loss.

**Fix applied**: split `headerIndex` into the two passes described above
— pass 1 claims every field it can via the original exact/bracket rule
before pass 2 is allowed to touch anything, so a real exact match anywhere
on the row always outranks a lenient match anywhere else. Re-verified
against the round-1 reviewer's own measured cases (now correct) plus all
original #587 acceptance tests (unaffected — the fallback still fires
whenever nothing claims the field in pass 1).

Non-blocking observations from round 1, triaged:

- **N1 (accepted, not fixed)** — `hasColumn`'s new leniency also widens
  `DetectFormat`'s `department` detection branch (its only caller), e.g.
  a `"Dept (code)"` header now counts. Real, but low practical risk (no
  real SumUp export carries a department column) and arguably in scope
  per the issue's own text ("every field in `columnSynonyms` has the same
  brittleness"). Not fixed in this diff; noted for a future card if it
  ever bites.
- **N2 (accepted, not fixed)** — the diff removes one now-redundant
  literal (`"tax rate (%)"`) but leaves other pre-existing redundant
  literals untouched (`"sold by weight (y/n)"`, `"price [gbp]"`).
  Genuinely out of scope for this card — those predate #587 and aren't
  part of what it's fixing; touching them would be unrelated cleanup in a
  bug-fix diff.
- **N3/N4/N5 (fixed)** — no direct unit test for `stripTrailingParen`'s
  edge cases, no guard against an empty stripped key, no negative test
  proving the fallback can't shadow an exact match. N5 became the B1 fix
  test; N4's guard (`if key == "" { continue }`) is now explicit in pass
  2 rather than correct only because no synonym is `""`. N3 (a fully
  standalone table test of `stripTrailingParen` in isolation) was judged
  adequately covered by the shadow test's own edge-case exercise
  (empty-paren, unbalanced-paren behaviour) rather than added separately
  — a judgement call, not a disagreement with the finding.
- **N6 (fixed)** — a stale test comment in `TestParseSumUp` said
  `"Tax rate (%)"` "doesn't match the pre-existing synonym set's exact/
  bracket-prefix rule," true but no longer explaining *how* it does
  resolve post-fix. Reworded to name the paren-stripped fallback.
- **N7** — no review record existed yet in the WIP snapshot the round-1
  reviewer saw; this file is that record.

**Round 2 — scoped to the B1 fix only** (earned per this pipeline's
"a second round is earned by a blocker-class finding" rule — money/tax
silent corruption qualifies). Verdict: **SAFE TO MERGE.** Independently
reproduced the B1 regression against the pre-fix code (matching round 1's
110-vs-450 and 700-vs-1900 numbers exactly), confirmed the two-pass split
closes it, and — going further than round 1's own two measured cases —
found the shadowable blast radius was actually **four** fields
(`price`, `tax`, `takeaway_tax`, `stock`), all closed by the same generic
fix (it's a matching-order fix, not a field-specific patch, so this isn't
a new gap — just wider proof the fix is correct at the right level).
Checked the two-pass split for new problems of its own (map-iteration
determinism, the `taken`-check placement in both passes, the dropped
bracket-prefix check in pass 2 being a genuine no-op since pass 1 already
covers it) — none found. Full gate green.

## Verified beyond automated tests

- **TDD claim, both fixes, verified independently, not taken on trust**:
  for the original #587 fix, reverted only `catimport.go` to `main`,
  confirmed all four new-test subtests failed with real assertion
  failures (not compile errors), restored, confirmed passing. For the B1
  fix, same procedure against the pre-B1-fix commit — confirmed
  `TestHeaderMatchingParenSuffixNeverShadowsAnExactMatch` fails with the
  exact 110-vs-450 / 700-vs-1900 mismatch, then passes after restoring.
- **Removed-literal safety**: confirmed `"Tax rate (%)"` (the removed
  literal) is fully subsumed — regression-guarded by the pre-existing
  `TestParseSumUp`/`sumupCSV` fixture and `TestDetectFormatSumUpFallbackSignature`,
  both green post-fix.
- **No other call sites affected**: `internal/pages/import_page.go` and
  `import_dispatch.go` (catimport's only consumers) use only the public
  `Parse`/`DetectFormat` API, unchanged signatures — confirmed via grep,
  no direct reach into `headerIndex`/`hasColumn`.
- Whole-repo `go build ./...`, `go vet ./...`, full `go test ./...` (37
  packages), and `guard-data-access.sh`/`guard-i18n.sh` all green, run
  independently by both review rounds, not just trusted from the
  implementation step.

## N/A for this diff

Pure internal Go parser package: no SQL (guard-data-access confirms), no
i18n strings (package has no locale by design — reason codes only,
guard-i18n confirms), no money-type conversion (raw `int64` minor units
is correct at this parse-layer boundary), no file I/O, no plugin loading,
no UI surface — none of `universal-till/CLAUDE.md`'s money/i18n/
offline-first/plugin/file-write checks apply.

## Deferred / follow-ups (not chased in this diff)

- N1: `hasColumn`'s widened leniency inside `DetectFormat`'s department
  branch — real but low-risk, no card opened yet since no concrete export
  is known to trigger it.
- N2: other pre-existing redundant literal synonym entries
  (`"sold by weight (y/n)"`, `"price [gbp]"`) — out of scope for this
  card, left as-is.
- A qualified header claiming a field when it's the *only* candidate on
  the row (e.g. `Name,Price (cost)` alone → cost price read as retail) is
  inherent to the paren-stripping mechanism itself, not a gap the B1 fix
  introduced or could close — the B1 fix only arbitrates when a genuine
  exact match exists elsewhere on the same row. Noted as a known,
  accepted limitation rather than a defect.

## Safe to merge

Yes, after the B1 fix. The original diff's core mechanism (paren-suffix
stripping) and its four acceptance-criteria tests were correct from round
1; the blocking issue was strictly about match *precedence*, fixed by
making the lenient pass strictly lower-priority than the exact pass, and
independently re-verified in a second, scoped round per this pipeline's
process rules.
