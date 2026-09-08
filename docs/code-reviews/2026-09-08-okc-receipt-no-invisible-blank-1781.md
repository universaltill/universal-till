# Code review — OKC receipt_no trim accepts a zero-width-space-only value (ut-docs#1781)

**Date:** 2026-09-08
**Branch:** `fix/1781-okc-receipt-no-invisible-blank`
**Reviewer:** independent fresh-context Sonnet subagent (complexity:easy —
a different, clean-context instance of the same model that wrote the fix),
isolated in its own git worktree
**Verdict:** SAFE TO MERGE AS-IS — no blocking findings, two non-blocking
notes recorded below.

## The bug

Split out of ut-docs#1763's independent review (finding F4). Both
`internal/fiscal.DeviceEvidence.Valid()` and `plugins/tax-tr/okc.BridgeDriver`'s
`Sale`/`Refund` decided whether a device-reported receipt number counts as
usable evidence with `strings.TrimSpace(receiptNo) != ""` (or `== ""`).
`strings.TrimSpace` only strips runes where `unicode.IsSpace` is true, which
does **not** cover Unicode category Cf ("format") characters such as U+200B
(ZERO WIDTH SPACE) or U+FEFF (BOM/ZWNBSP). A `receipt_no` made up only of
such characters therefore passed both checks and was accepted/persisted as
a "valid" receipt that renders blank on every screen and report.

## The fix

Both call sites now use a small `isBlankReceiptNo` helper — a string is
blank only when every rune in it is whitespace (`unicode.IsSpace`) **or**
category Cf (`unicode.In(r, unicode.Cf)`) — deliberately duplicated between
`internal/fiscal` and `plugins/tax-tr/okc` rather than shared via import:
this plugin package already avoids importing `internal/` code anywhere
(mirrors `internal/fiscal.DeviceEvidence`'s JSON field names into its own
`Evidence` type instead of importing it), keeping it extractable into its
own module/repo later per ADR-0009.

## What the reviewer verified independently

- **Build/test/fmt/lint, actually run, not read**: `go build ./...` clean;
  `go test ./internal/fiscal/... ./plugins/tax-tr/okc/... -v` — all pass,
  including the 3 new cases; `gofmt -l` on the 4 changed files — clean;
  `golangci-lint run ./internal/fiscal/... ./plugins/tax-tr/okc/...` — 0
  issues; full `go test ./...` — every package green (`internal/pages`
  158.9s, `internal/data` 45.5s, `internal/plugins` 87.0s included).
- **TDD claim re-verified independently, not trusted**: reverted only the
  two non-test files to their pre-fix versions (tests kept), rebuilt
  (clean compile), reran the new tests — real assertion failures, not
  compile errors (`ok = true, want false`, `err = <nil>, want ErrNoReceipt`).
  Restored the fix, reran — green again.
- **The Unicode-category claim itself checked, not trusted from the PR
  description**: a standalone Go snippet confirmed U+200B and U+FEFF (plus
  LRM/RLM/ZWJ/ZWNJ/soft-hyphen/LRE/PDF) all report `IsSpace=false,
  In(Cf)=true` — the fix's premise holds.
- **Over-aggressiveness checked**: `isBlankReceiptNo` only returns true
  when *every* rune is whitespace-or-Cf, so a real receipt number
  (ASCII digits, or non-Cf scripts like Arabic-indic digits) with an
  incidentally embedded Cf character still passes — only an all-invisible
  string is rejected.
- **Duplication justification checked, not just accepted**: grepped all of
  `plugins/` for any `universal-till/internal` import — zero hits. The new
  comment's isolation rationale matches the actual codebase, not a
  special-cased excuse for this diff.
- No file-write/`os.MkdirAll`/`paths.Data(...)` concern (pure string/rune
  logic, no I/O). No secret-shaped literal or real client/shop name. No UI
  surface or shop-owner-visible behavior touched, so the UX-guideline and
  user-manual gates correctly do not apply here.

## Non-blocking notes (recorded, not fixed here — out of scope for this card)

1. **`ParseDeviceEvidence`'s post-`Valid()` `strings.TrimSpace`
   (device.go) still only strips ASCII/Unicode whitespace, not Cf.** A
   `receipt_no` like `"​123​"` (real digits sandwiched between
   zero-width spaces) correctly passes `Valid()` (not all-blank) but keeps
   the invisible characters at its edges in the stored value — a narrower
   data-hygiene gap than the all-invisible case this card reports (the
   visible text still renders correctly, since ZWSP is truly zero-width,
   so it wouldn't reproduce the reported symptom), but worth a follow-up
   for exact-match lookups/reprint keys. Filed as a new Backlog card
   rather than folded into this fix.
2. **Test cases use two zero-width spaces (`"​​"`) rather than
   one** in both `device_test.go` and `bridge_test.go` — verified
   byte-for-byte, not a copy-paste mismatch (both files use the identical
   escape twice), just a cosmetic choice; a single character would have
   been the minimal reproduction. Not worth a re-request.

## Verified beyond automated tests

N/A — this is an internal validity predicate with no device-hardware or
UI-runtime dependency; the automated test suite (host-run, real JSON
parsing and real TCP loopback to the `okc/sim` simulator) is the
appropriate verification surface for this change.
