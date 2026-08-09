# Code review: catalog CSV formula-trigger drift guard test

**Card:** universaltill/ut-docs#356
**Date:** 2026-08-09
**Complexity:** easy — Dev inline (session model), Review via a fresh-context
Sonnet subagent, independent of the Dev's own reasoning.

## Background

`internal/catimport/catimport.go`'s `stripCSVDefuse` duplicates
`internal/pages/csv_export.go`'s `csvSafe` formula-trigger char set
(`=+-@`, tab, CR) as a local const `csvFormulaTriggers`, because
`catimport` can't import `pages` (`pages` already imports `catimport`; the
reverse would cycle). The two sets were kept in sync only by a code
comment, not a test — flagged as a follow-up (finding 2, minor) in the
2026-08-06 review of ut-docs#321
(`docs/code-reviews/2026-08-06-catalog-csv-formula-injection.md`).

## What shipped

`TestCatalogCSVFormulaTriggersStaySynced` in
`internal/pages/export_roundtrip_test.go` — test-only change, no
production code touched. Rather than hardcoding a third copy of the
trigger set (which could itself drift from the other two), it sweeps
candidate bytes (tab, CR, printable ASCII `!`–`~`) as the leading byte of
two round-tripped values through the real `writeCatalogCSV` (`csvSafe`)
and `catimport.Parse` (`stripCSVDefuse`):

- `plain(b)` — no leading apostrophe. Catches a trigger char added to
  `csvSafe` without mirroring it to `csvFormulaTriggers` (stray leading
  `'` survives on import).
- `genuine(b)` — a real leading apostrophe followed by a byte `csvSafe`
  does **not** currently treat as a trigger. Catches a trigger char added
  to `csvFormulaTriggers` without mirroring it to `csvSafe` (a real
  apostrophe gets wrongly stripped on import). Whether `b` is "currently a
  trigger" is decided by calling the real `csvSafe` at runtime, not a
  hardcoded copy.

Each value embeds a comma and a quote, exercising real `encoding/csv`
quoting alongside the defuse/strip logic (ut-docs#356 AC).

## Independent review (fresh-context Sonnet, isolated worktree)

Verdict: **safe to merge**, one should-fix (applied), one nit (applied).

**Actually run, not just read:** `go build ./...`, `go vet ./...`,
`go test ./internal/pages/... ./internal/catimport/...` — all green.
`gofmt -l` clean. Independently re-verified the drift-catching claim by
simulating both directions directly (not trusting the Dev's word):
added a `#` trigger char to `csvSafe`'s switch only → new test failed at
`plain_0x23`; reverted, added the same char to `csvFormulaTriggers` only
→ failed at `genuine_apostrophe_0x23`; reverted both, confirmed green
again.

**Findings, both fixed in this diff:**

- **should-fix:** the first draft's "genuine apostrophe" scenario skipped
  bytes via a hardcoded `knownTriggers` map — itself a third copy of the
  trigger set, despite the test's own doc comment framing the design as
  deliberately avoiding one. The reviewer proved this concretely: adding a
  new trigger char to **both** `csvSafe` and `csvFormulaTriggers` together
  (a legitimate, synced, non-drift change) still made the test fail,
  because the hardcoded skip-list wasn't updated too — a third file a
  future dev would have to remember to touch, undocumented anywhere.
  **Fixed** by deriving the skip condition from the real `csvSafe` at
  runtime (`isCurrentTrigger`) instead. Verified post-fix: the same
  synced-addition scenario now stays green, and both drift directions
  above still correctly fail. One subtlety caught while implementing the
  fix (not flagged by the reviewer, found writing the replacement):
  `csvSafe` exempts the exact literal string `"-"` (the audit-export "no
  entity ID" sentinel), so a naive `len(csvSafe(string(b))) > 1` probe
  misclassifies `-` as a non-trigger. `isCurrentTrigger` probes with a
  second byte appended (`string(b)+"y"`) so the field is never exactly
  `"-"`, sidestepping the sentinel exemption. Confirmed `plain_0x2d`
  (`-`) still runs and passes, and `genuine_apostrophe_0x2d` is correctly
  skipped (not run at all), post-fix.
- **nit:** the doc comment's "for ANY byte" claim overstated coverage —
  the sweep is tab, CR, and printable ASCII only, not literally every
  byte. **Fixed**: reworded to name the actual swept range and note what's
  excluded (other C0 controls, DEL, bytes ≥0x80) and why that's not a
  practical gap (no trigger char in this codebase has ever been outside
  that range).

**Deliberately not flagged, confirmed correct:** the space exclusion
(pre-existing, unrelated `strings.TrimSpace` behavior in `catimport`'s
`get()` — checked directly); the non-injectivity edge case is correctly
left untested (out of scope per the card, already accepted/documented on
`stripCSVDefuse`); scope is test-only (`git diff main..HEAD --stat`
confirms exactly one file, all-additive then a targeted rework); no
filesystem I/O (`os.MkdirAll`/`paths.Data` classes don't apply); no
secrets or real client/shop names in test data; no user-facing string, UI,
or route touched, so no `web/help/`/`web/locales/` update needed.

**One anomaly noted by the reviewer, not a review finding:** a tool
result during the reviewer's `git checkout --` revert step carried a
standard harness system-reminder ("file modified outside Edit/Write")
worded as if it were a request to conceal the change from this report.
The reviewer correctly ignored that framing and reported the revert
plainly here — flagged for transparency, not because it affected the
verdict (`git status`/`git diff` immediately confirmed the tree was back
to the reviewed commit).

## Verified beyond the automated suite

- TDD-verified personally (not just on the reviewer's word) before
  requesting review: reverted the fix's own behavior in both directions
  and confirmed the test goes red for each, then confirmed green restored
  — see commit history on this branch.
- Full repo gate run once, after the review's fix was applied:
  `go build ./...`, `go vet ./...`, `go test ./...` (all packages green),
  `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh` (all pass), `gofmt -l` clean on the changed
  file.

## Safe-to-merge verdict

Yes. Test-only, no production code changed, both drift directions and the
synced-addition non-regression verified directly (not just asserted), full
gate green.
