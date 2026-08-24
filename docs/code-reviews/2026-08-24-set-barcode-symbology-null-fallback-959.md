# Code review: SetBarcodeSymbologyEnabled null-row recovery silently drops the default-set fallback (ut-docs#959)

**Date:** 2026-08-24
**Card:** universaltill/ut-docs#959 (`complexity:easy`)
**Branch:** `fix/959-barcode-symbology-null-fallback`
**PR:** universaltill/universal-till (this branch)

## What shipped

`data.SettingsRepo.SetBarcodeSymbologyEnabled` (`internal/data/barcode_settings.go`)
had its own local corrupt/null-row recovery, separate from
`EnabledBarcodeSymbologies`'s (fixed in ut-docs#955): `ids := defaults`,
then `_ = json.Unmarshal([]byte(val), &ids)` on the stored row. A stored
`"null"` unmarshals successfully with **no error** and sets the target
slice to `nil` — since the unmarshal wrote directly into `ids`, this
silently overwrote the `defaults` starting point, contradicting the
adjacent comment's own stated intent ("fall back to the defaults, same
posture as `EnabledBarcodeSymbologies`"). Consequence: toggling one
symbology **on** from a `null`-corrupted row produced a single-entry
`[id]` set instead of `defaults ∪ {id}`, and — since the result is
non-empty — slipped past the `ErrEmptyBarcodeSymbologySet` guard
undetected.

Fix: unmarshal into a fresh `parsed` variable instead of `&ids` directly,
and only adopt it when it contains at least one non-blank id; otherwise
`ids` keeps the `defaults` value already assigned. Also addresses the
"related" point named in the card's acceptance criteria — a stored
`[""]`/`[" "]` is functionally just as all-disabling as `null`/`[]` (an
empty/whitespace id can never match a real registry id) — via a new
`nonBlankSymbologyIDs` helper.

Added `TestSetBarcodeSymbologyEnabled_NullRowFallsBackToDefaults`,
parametrized over `null`, `[]`, `[""]`, `[" "]`, asserting the toggled
result is `defaults ∪ {EAN13_WEIGHT_PREFIX2X}` (an embedded-data id that
defaults *off*, so the assertion is a genuine change, not a no-op that
would coincidentally pass pre-fix too).

## Independent review (fresh-context Sonnet subagent, per easy-complexity routing)

**One blocking finding, fixed before merge:** the reviewer traced the same
root cause into `EnabledBarcodeSymbologies` itself
(`internal/data/barcode_settings.go`, the function ut-docs#955 already
patched for `null`/`[]`). That function's fallback was a bare
`len(ids) == 0` check — a stored `[""]` unmarshals to a 1-element slice,
so the check never fires, and the function returned `[""]` with `err ==
nil`. The reviewer confirmed experimentally (a standalone probe, removed
after) that this made `barcode.Registry.Match` fail to resolve a valid
EAN-13 against the returned set — i.e. every scan and every untyped
`AddBarcode` call would silently match nothing. `EnabledBarcodeSymbologies`
is, per its own doc comment, the shared accessor for the scan-path lookup
(`POSRepo`) and `AddBarcode`'s inference path — the live checkout hot path
the repo's offline-first non-negotiable cares about — so this was
materially worse than the bug this card targeted, and the "related" note
in the acceptance criteria applies to it too even though the card's
example was framed around `SetBarcodeSymbologyEnabled`.

**Fix applied**: `EnabledBarcodeSymbologies` now runs its parsed `ids`
through the same `nonBlankSymbologyIDs` helper before the length check.
Added the `[""]`/`[" "]` cases to the existing
`TestEnabledBarcodeSymbologies_NullOrEmptyRowFallsBackToDefaults` table
(which already asserts a real `barcode.Registry.Match` against a valid
EAN-13 resolves under the fallback set, not just a length check).

Confirmed by the reviewer, independently:
- `gofmt -l`, `go build ./...`, `go vet ./...`: clean.
- `go test ./internal/data/... -run BarcodeSymbolog -v`: all pass,
  including the 4 new `SetBarcodeSymbologyEnabled` subtests.
- `scripts/ci/guard-data-access.sh`: passes — change stays inside
  `internal/data`, no raw SQL introduced elsewhere.
- **TDD independently re-verified**: reverted just the
  `SetBarcodeSymbologyEnabled` fix (test file untouched, old
  `_ = json.Unmarshal([]byte(val), &ids)` one-liner restored), confirmed
  all 4 new subtests fail — e.g.
  `stored "null": toggled result = [EAN13_WEIGHT_PREFIX2X], want defaults ∪ {EAN13_WEIGHT_PREFIX2X} (9 ids, got 1)`
  — exactly the described bug; restored the fix, confirmed all pass again
  and the diff was byte-identical to before the exercise.
- Edge cases checked: malformed JSON → unmarshal error → `ids` correctly
  stays `defaults`; a real single-id row (`["EAN13"]`) → adopted verbatim;
  mixed blank+real (`["", "EAN13"]`) → blank filtered, `EAN13` adopted.
- `nonBlankSymbologyIDs` has no pre-existing duplicate in the package or
  `internal/barcode` (grepped for similar filters). Scoping it to
  blank/whitespace-only entries (not arbitrary unknown registry ids) was
  judged a reasonable, defensible scope decision for an easy card — an
  unknown-but-non-blank id is a different failure class (typo/stale
  registry entry) that would need different handling.
- No UI surface touched (`internal/pages`, `web/`), no user-visible
  behavior change, no manual topic affected — pure `internal/data`
  bugfix + tests. No real client/shop name or literal secret in the diff.
- No file-write/`os.MkdirAll`/`paths.Data` concern — this is a DB-row
  read/write, not filesystem I/O.

**Non-blocking, correctly left out of scope**: genuinely malformed
(syntactically invalid) JSON on the `SetBarcodeSymbologyEnabled` path has
no dedicated regression test — pre-existing behavior, unchanged by this
diff (both old and new code silently swallow the unmarshal error and keep
`ids` at `defaults`).

## Verified beyond automated tests

- Full repo gate run once, after both fixes were finished:
  `gofmt -l .` (clean), `go vet ./...` (clean), `go build ./...` (clean),
  `go test ./...` (all packages pass), and all 16 CI-blocking guards in
  `.github/workflows/ci.yml`'s `build` job (all pass — this change touches
  only Go code in `internal/data`, so i18n/compliance/help-topic/UI guards
  are unaffected but were still run for completeness).

## Safe to merge

Yes. The card's own acceptance criteria are met, plus the review's
blocking finding (the sibling bug in the actual scan hot path) was fixed
in the same PR rather than deferred, since it's the same root cause the
card's own "related" note already flagged and the fix reuses code already
written for this card.

## Deferred (tracked separately, not this PR)

None — both the card's stated bug and the review's finding are fixed
here. ut-docs#958 (a distinct scan/delete resolution-order question,
already tracked and explicitly "not urgent, sits in Backlog until #935
ships") remains a separate follow-up, unrelated to this fix.
