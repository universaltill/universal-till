# Code review: receipt/kitchen-ticket padding math counts bytes, not runes

**Ticket:** universaltill/ut-docs#376
**Date:** 2026-08-08
**Repo/branch:** `universal-till` / `fix/receipt-padding-rune-count`
**Reviewer:** independent fresh-context Sonnet subagent (complexity:easy tier), isolated worktree

## What shipped

Three padding/centering helpers in `internal/print` computed row width
using Go's `len()` (byte length) against strings that can contain
multi-byte UTF-8 characters (£, ä/ö/ü/ß, ar/fa/tr text). Byte length
overcounts visible column width for any such string, so the row ended up
under-padded — narrower than the fixed `Width = 42` column layout
actually intends. Right-alignment/centering silently drifted off target
for any non-ASCII locale's printed receipts and kitchen tickets.

- `kvRow` (`escpos.go`): right-aligns an amount against a label. Padding
  calc changed from `len(label)`/`len(amount)` to
  `utf8.RuneCountInString(...)`.
- `RenderText`'s `center` closure (`escpos.go`): plain-text receipt
  preview renderer. Same fix.
- `RenderKitchenTicketText`'s `center` closure (`kitchen.go`): plain-text
  kitchen-ticket preview renderer. Same fix, plus the `unicode/utf8`
  import added to that file.

This mirrors the convention the neighboring `clip()` helper already
established for the same bug class (byte-vs-rune truncation, fixed
earlier for ut-docs#371).

### Tests (written test-first, TDD)

- `TestKvRowPadsByRuneCountNotByteCount` — confirms a label/amount pair
  containing `£` pads to the full rune-width row and the amount lands
  flush at the end, not one column short.
- `TestRenderTextCentersByRuneCountNotByteCount` — a store name with two
  distinct multi-byte characters (`Café Bar Français`, chosen so a
  byte-based and a rune-based integer division land on *different*
  results, not the same one by rounding coincidence) must center at
  exactly the rune-count-derived pad.
- `TestRenderKitchenTicketTextCentersByRuneCountNotByteCount` — same
  pattern for the kitchen-ticket renderer.
- `TestRenderStructure`'s existing assertion corrected from
  `len([]byte(r)) >= Width` (byte width — see review finding below) to
  `utf8.RuneCountInString(r) >= Width` (visible width), so it can't mask
  this bug class again.

All four were confirmed failing against the pre-fix code with the real,
on-topic error messages before the fix landed, then passing after.

## Independent review (round 1)

An independent, fresh-context Sonnet subagent, isolated in its own git
worktree, reviewed the diff without having seen any prior reasoning
about it:

- Ran `go build`, `go vet`, `go test ./internal/print/... -v`, the full
  `go test ./...`, and `gofmt -l internal/print/` itself — all clean.
- **Independently re-verified the TDD claim**: reverted just the three
  production-code fixes (kept the tests), re-ran the four tests, got the
  real failing output quoted above, then restored the fix and confirmed
  green again.
- Found `TestRenderStructure`'s *original* byte-based assertion was
  tautologically true both before and after the fix (`kvRow` always
  produces a row whose *byte* length equals `Width` when `space >= 1`),
  so it never actually caught this bug — confirmed the corrected
  rune-based assertion genuinely strengthens the test (fails against the
  reverted/buggy code at 41 runes, one short of `Width`).
- Traced `kvRow`'s re-clip path (`clip(label, Width-utf8.RuneCountInString(amount)-1)`)
  for a multi-byte `amount` by hand and confirmed no off-by-one — the
  re-clipped row always lands at exactly `Width` runes.
- Searched all of `internal/print` and every external importer
  (`internal/pages/{print_api,receipt_designer,kitchen_print,invoice_page,eod_api,eod_test}.go`)
  for other `Width`-relative `len()` patterns with the same bug class —
  found one (see below), nothing else.
- Confirmed no real client/shop name in test data (`Coca-Cola`, `Café
  Bar Français`, `Grill Café Français`, `Café Zürich`, `Task Runner`).
- Confirmed no manual-doc topic references print column
  alignment/padding — this is backend text-stream formatting for
  physical printers, not a UI screen, so nothing in `web/help/` goes
  stale.
- Confirmed N/A for the two recurring bug classes (missing
  `os.MkdirAll`, cwd-relative path instead of `paths.Data`) — this diff
  touches no file I/O or path handling.

### Findings

1. **Non-blocking, fixed in this round**: a test comment in
   `kitchen_test.go` stated the wrong byte count and character
   breakdown for the sample multi-byte string (said 22 bytes / two `ç`s;
   actually 21 bytes / one `é` + one `ç`). The assertion itself computes
   its expected value directly via `utf8.RuneCountInString`, so
   correctness was never affected — fixed the comment only.
2. **Non-blocking, deferred to a new backlog card**: `layoutLine`'s
   one-row-vs-two-row threshold check (`escpos.go`, `if len(label)+1+len(l.Amount) <= Width`)
   still uses byte length, inconsistent with the now-rune-based `kvRow`
   it calls. Byte length is always `>=` rune length, so this can only
   ever be *over*-conservative — it may route a multi-byte label/amount
   combo sitting exactly on the column boundary to the two-row fallback
   when it would actually have fit on one row by visible-column count.
   Traced through: this never produces misalignment, truncation, or
   overflow (the two-row fallback path is itself correctly rune-safe) —
   worst case is one extra printed row (paper-usage cosmetic) for a
   narrow band of long multi-byte product names. Same bug class as this
   ticket, worth fixing for consistency, but not the misalignment defect
   ut-docs#376 is about and not required for this fix to be correct.
   Filed as universaltill/ut-docs#438.
3. **Informational, no action**: `TestLongNameWraps` (pre-existing,
   untouched by this diff) asserts
   `strings.Contains(out, strings.Repeat(" ", Width-len("£1.00"))+"£1.00")`
   — still byte-based, and only passes because a run of N identical
   space characters trivially contains any shorter run of the same
   character as a substring, not because it verifies exact alignment.
   Predates this diff, same bug class, folded into the same follow-up
   card (#438) rather than a second card.

No blocker-class issue (money/tax, data loss, security) — no second
review round.

## Verification performed (this session, after the fix)

- `go build ./...`, `go vet ./...` — clean.
- `go test ./internal/print/... -count=1 -v` — all 22 tests pass.
- `go test ./...` — every package passes except the pre-existing,
  unrelated `TestSaveCleansUpDirectoryOnWriteFailure`
  (`internal/issuereport`), which fails under this sandbox's root-run
  environment (`chmod 0500` doesn't block root) — confirmed present on
  `main` before this change too (tracked separately as ut-docs#415; the
  independent reviewer separately re-confirmed this on a clean `main`
  worktree).
- `bash scripts/ci/guard-data-access.sh`, `guard-i18n.sh`,
  `guard-help-topics.sh` — all green (none of the three are really
  exercised by this diff — no SQL, no user-facing template strings, no
  page routes touched — but all three were run per the standard gate).
- `gofmt -l internal/print/` — clean.

## Scope

Backend text-stream formatting for physical receipt/kitchen-ticket
printing only (`internal/print`) — no SQL/data-access, no money math, no
user-facing template string, no HTTP handler, no UI screen, no
plugin-loading/verification path touched. No manual-doc update needed.

## Outcome

Independent review found no blocking issues. One trivial comment
inaccuracy fixed in this round; one real-but-non-defect follow-up
(`layoutLine`'s byte-based threshold, plus a pre-existing weak assertion
in `TestLongNameWraps`) filed as universaltill/ut-docs#438 rather than
expanded into this PR's scope.

Safe to merge.
