# Code review: layoutLine's wrap threshold uses rune count, not byte length (ut-docs#438)

**Date:** 2026-08-08
**Card:** universaltill/ut-docs#438
**Complexity:** easy
**Repo/branch:** `universal-till`, `fix/layoutline-rune-width-438`

## What shipped

`internal/print/escpos.go`'s `layoutLine` decides whether a sale line
(`"qty x name"` + amount) fits on one printed row or needs a two-row
fallback. The threshold compared `len(label)+1+len(l.Amount) <= Width` —
Go's `len()` on a string is byte length, not the visible-column count
`Width` (42) actually means. `kvRow`, which `layoutLine` calls for the
one-row case, already pads by `utf8.RuneCountInString` (fixed under
ut-docs#376), so the two were inconsistent: a multi-byte label/amount
pair (é/ö/ü/ß, or Arabic/Farsi/Turkish product names) sitting between the
rune-count width and the byte-count width got routed to an unnecessary
two-row wrap even though `kvRow` could render it correctly on one row.
Confirmed cosmetic (extra paper use) — not a truncation/misalignment/data
bug — traced through both call sites (`Render` and `RenderText`).

Fix: threshold changed to
`utf8.RuneCountInString(label)+1+utf8.RuneCountInString(l.Amount) <= Width`.

Tests:
- New `TestLayoutLineRuneBoundaryFitsOneRow`: a 20×`é` label (20 runes /
  40 bytes) + `£1.00` amount (5 runes / 6 bytes) — rune total 26 fits
  Width(42), byte total 47 doesn't. Asserts `layoutLine` returns exactly
  one row, right-padded to exactly `Width` visible columns.
- `TestLongNameWraps` tightened from a `strings.Contains` substring check
  (which passed even pre-fix, for the wrong reason — a run of N spaces
  trivially contains any shorter run of the same character) to an exact
  row-content match.

## Independent review

Fresh-context Sonnet subagent (complexity:easy → same-tier reviewer per
the pipeline's model-routing rule), isolated in its own git worktree.

**Verdict: safe to merge, no blockers.**

Checked and cleared: threshold math (no off-by-one), the second call site
(`RenderText`), test fragility, CLAUDE.md compliance (no SQL/money/i18n/
file-I/O/path/plugin/route surface touched), the two recurring bug
classes this pipeline watches for (missing `os.MkdirAll`, cwd-relative
path instead of `paths.Data(...)` — both N/A, no file I/O in this diff),
demo/secret literals (none), and manual/help-topic implications (none —
internal print-formatting has no operator-facing screen or `web/help/`
topic to update).

Two informational, non-blocking notes recorded, not fixed (out of
scope for this card):
- The fix uses rune count, not true display width, so a double-width CJK
  glyph would still count as one column. This matches the existing
  convention across the whole file (`clip`, `kvRow`, `RenderText`'s
  `center`, kitchen-ticket helpers all use `utf8.RuneCountInString` the
  same way, per ut-docs#371/#376) — not a regression, and this card's
  own scope is explicitly the é/ö/ü/ß/ar/fa/tr case, not CJK. No backlog
  card filed; this is the same pre-existing convention the rest of the
  file already accepted, not new debt introduced here.
- The new regression test's rune/byte totals (26 vs. Width 42) demonstrate
  the discrepancy generally rather than pinning exactly on the `==Width`
  edge. Its own `t.Fatalf` setup assertions make it robust regardless — a
  naming nit at most.

### TDD claim independently re-verified

The reviewer reverted *only* the `layoutLine` threshold back to the
byte-length comparison (new tests left untouched), confirmed
`TestLayoutLineRuneBoundaryFitsOneRow` fails with the exact predicted
symptom (2 rows instead of 1), confirmed every other test in the package
(including the tightened `TestLongNameWraps`) still passes unaffected,
then restored the fix and confirmed a clean `git diff` against the
reviewed commit and a fully green suite.

### Verified beyond automated tests

- `go build ./...`, `go vet ./...` — clean, full repo.
- `go test ./...` — full repo green (all packages).
- `go test ./internal/print/... -race -v` — 25/25 pass.
- `gofmt -l internal/print/` — clean.
- `bash scripts/ci/guard-data-access.sh` — pass.
- `bash scripts/ci/guard-i18n.sh` — pass.
- `bash scripts/ci/guard-help-topics.sh` — pass.
- `golangci-lint run ./internal/print/...` — 4 pre-existing `errcheck`
  findings on unrelated lines, confirmed identical on `main` before this
  diff (not introduced here).

## Deferred / follow-up

None filed — both reviewer notes above are accepted existing convention,
not new debt from this change.
