# Code review: null/empty enabled-symbologies row silently disables all barcode matching (ut-docs#955)

**Date:** 2026-08-24
**Card:** universaltill/ut-docs#955 (`complexity:easy`)
**Branch:** `fix/955-null-barcode-symbologies-fallback`
**PR:** universaltill/universal-till (this branch)

## What shipped

`data.SettingsRepo.EnabledBarcodeSymbologies` (`internal/data/barcode_settings.go`)
unmarshalled the stored `barcode_enabled_symbologies` setting value into
`[]string`. A stored `"null"` or `"[]"` unmarshals successfully to a
nil/empty slice with **no error**, bypassing the existing parse-error
fallback and yielding an all-disabling empty enabled set — every scan and
every untyped `AddBarcode` call would then fail to match anything, with no
error surfaced anywhere.

Fix: after a successful unmarshal, if the resulting slice is empty, fall
back to `DefaultEnabledBarcodeSymbologyIDs()` — same posture as the
existing corrupt-row fallback, but with `err == nil` since a `null`/`[]`
isn't a parse failure, matching the acceptance criteria exactly.

Added `TestEnabledBarcodeSymbologies_NullOrEmptyRowFallsBackToDefaults`
covering both a stored `"null"` and a stored `"[]"` row, asserting not just
the returned set's length but that a real scan (`barcode.Registry.Match`
against a valid EAN-13 checksum) resolves under the fallback set.

## Independent review (fresh-context Sonnet subagent, per easy-complexity routing)

No blockers or majors. Confirmed:
- Correctly distinguishes parse-failure (still returns the error) from
  successfully-parsed-but-empty (returns `nil` error) — matches the
  acceptance criteria.
- Checked all four current callers (`internal/data/pos_repo.go`,
  `internal/data/catalog_repo.go`, `internal/pages/import_page.go`,
  `internal/pages/settings_page.go`) — none depend on getting back an
  empty slice with a nil error; the behavior change is safe for all of
  them.
- **TDD independently re-verified**: reverted just the `.go` fix (test
  kept in place), confirmed both new subtests fail with
  `fallback = [], want the default set [...]`; restored the fix, confirmed
  both pass again.
- Test quality: matches existing file conventions
  (`seedSettingsTable`/`testsupport.NewCatalogTestDB`), and exercises the
  acceptance criterion's actual intent (a real scan/AddBarcode-equivalent
  match resolving under the fallback set), not just a length check.
- `gofmt -l`, `go vet ./internal/data/...`, `go build ./...`: clean.
- `scripts/ci/guard-data-access.sh`: passes — no raw SQL introduced
  outside `internal/data`/`internal/db`.

Two informational findings, both explicitly out of scope for this card
(acceptance criteria named only `EnabledBarcodeSymbologies`, `"null"` and
`"[]"`) — filed as a follow-up backlog card rather than widening this fix:

1. **Related bug, same root cause, different function.**
   `SetBarcodeSymbologyEnabled`'s local corrupt-row recovery
   (`_ = json.Unmarshal([]byte(val), &ids)`) silently overwrites its
   `ids := defaults` starting value when the stored row is `"null"`,
   contradicting its own adjacent comment ("fall back to the defaults,
   same posture as `EnabledBarcodeSymbologies`"). A toggle-on from a
   `null`-corrupted row would produce a single-entry set instead of
   `defaults ∪ {id}`, and — since the result is non-empty — it slips past
   the `ErrEmptyBarcodeSymbologySet` guard undetected.
2. **Edge case not in the acceptance criteria.** A stored `[""]` (one
   element, empty string) has `len(ids) == 1`, so it skips this fix's
   fallback, yet is functionally just as all-disabling since `Match` can't
   resolve an empty/unknown id against any real code.

## Verified beyond automated tests

- Full repo gate run once, after the fix was finished:
  `gofmt -l .` (clean), `go build ./...` (clean),
  `go test ./...` (all packages pass), and all 16 CI-blocking guards in
  `.github/workflows/ci.yml`'s `build` job (all pass — this change touches
  only Go code in `internal/data`, so i18n/compliance/help-topic/UI guards
  are unaffected but were still run for completeness).
- Read every current caller of `EnabledBarcodeSymbologies` to confirm none
  relied on the pre-fix nil-slice-with-no-error behavior.

## Safe to merge

Yes. Minimal, correctly scoped to the acceptance criteria, genuinely
TDD-verified, no caller regressions, all guards green.

## Deferred (tracked separately, not this PR)

- Follow-up backlog card for the two informational findings above
  (`SetBarcodeSymbologyEnabled`'s sibling null-fallback gap, and the
  single-empty-string edge case) — filed against ut-docs.
