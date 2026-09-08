# Code review: DeviceEvidence.ReceiptNo retains invisible chars after normalization (ut-docs#1790)

**Date:** 2026-09-08
**Card:** ut-docs#1790
**Complexity:** easy
**Reviewer:** fresh-context Sonnet subagent (independent, no prior context on this change)

## Problem

`internal/fiscal.ParseDeviceEvidence` normalized `ev.ReceiptNo` with
`strings.TrimSpace` only, which strips Unicode whitespace but not
category-Cf "format" characters (U+200B ZERO WIDTH SPACE, U+FEFF
BOM/ZWNBSP, joiners, …). The sibling helper `isBlankReceiptNo` in the same
file already treats both classes as invisible when deciding *validity*, so
a `receipt_no` with real digits sandwiched between zero-width characters
at the edges (e.g. ZWSP + "123" + BOM) was correctly accepted as valid,
non-blank evidence — but was then persisted with those invisible edge
characters still attached. That stored value would not exact-match a
freshly typed/scanned "123" on a reprint-by-receipt-number or lookup path.

Split out of ut-docs#1781's independent review (which fixed the
all-invisible/fully-blank case); this card covers the narrower
edges-only case on an otherwise-valid receipt number.

## Fix

- Extracted the shared predicate `isInvisibleRune(r) = unicode.IsSpace(r)
  || unicode.In(r, unicode.Cf)` out of `isBlankReceiptNo` in
  `internal/fiscal/device.go`, so the blank-check and the trim can never
  drift apart on what counts as "invisible."
- `ParseDeviceEvidence` now does `strings.TrimFunc(ev.ReceiptNo,
  isInvisibleRune)` instead of `strings.TrimSpace`. `TrimFunc` only
  touches the edges, so any interior/embedded Cf character (not this
  card's scope — the card is specifically about edges, distinct from
  ut-docs#1781's all-invisible case) is left untouched.

## Tests

- New regression test `TestParseDeviceEvidenceStripsSandwichedInvisibleChars`
  in `internal/fiscal/device_test.go`: a `receipt_no` of `"
  ​123﻿ ​"` must parse as valid evidence with
  `ReceiptNo == "123"`.
- TDD confirmed personally (not taken on trust): reverted `device.go` to
  pre-fix, ran the new test — failed with `ReceiptNo =
  "​123﻿ ​", want "123"`. Restored the fix — passes.
  Re-confirmed independently by the review subagent via its own
  stash/pop, byte-identical.
- Full `go test ./internal/fiscal/...` and `go test ./...` (whole repo):
  all pass, zero regressions.
- `go build ./...`, `go vet ./...`, `gofmt -l .`, `golangci-lint run
  ./...`: all clean.
- `scripts/ci/guard-data-access.sh`: passes (no SQL touched).

## Independent review findings

Verdict: **SAFE TO MERGE**, no blocking issues. Full adversarial pass:

- Predicate correctness: `isInvisibleRune` is exactly the same predicate
  `isBlankReceiptNo` already used, so "valid" and "stored" are now
  definitionally consistent.
- No caller depends on the old unstripped behavior — traced every
  construction site of `fiscal.DeviceEvidence` (only ever built by
  `ParseDeviceEvidence`, consumed by `fiscal_device_hook.go`,
  `refund_page.go`, `pos_api.go`, and persisted via
  `internal/data/fiscal_device_repo.go`). Every other `ReceiptNo` field
  found via repo-wide grep belongs to unrelated types (`pos.Sale`,
  `OrderStatusChanged`, `data.SaleJournalEntry`, etc.) — correctly out of
  scope.
- Confirmed the new test is a real regression test, not a tautology (see
  TDD verification above).
- Confirmed `plugins/tax-tr/okc/bridge.go`'s own `isBlankReceiptNo` mirror
  correctly does NOT need the same `TrimFunc` treatment: traced the full
  data path — the bridge only uses its mirror to gate `ErrNoReceipt`, then
  returns the raw `Evidence` untouched; `plugins/tax-tr/main.go` marshals
  that raw evidence verbatim as `{"fiscal_device": {...}}` on stdout;
  core's plugin runtime hands that raw JSON to `fiscal.ParseDeviceEvidence`
  — the single real boundary where trimming happens exactly once. Verified
  by tracing code, not assumed.
- No lint/gofmt issues; no CLAUDE.md conflict (pure internal Go logic, no
  SQL, no kiosk-engine reference, no user-facing string, no compliance
  wording).

## Scope note (not fixed here, no new card needed)

`plugins/tax-tr/okc/bridge.go` never trims `ev.ReceiptNo` at all (not
even ordinary whitespace) before returning `Evidence` from its `Sale`/
`Refund` methods — but this is harmless: the raw evidence is marshaled to
JSON and always re-parsed through `fiscal.ParseDeviceEvidence` on the
core side, which now performs the one normalization pass that matters.
No separate fix needed.
