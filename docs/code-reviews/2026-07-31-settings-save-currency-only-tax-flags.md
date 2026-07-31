# Code review — currency-only settings save no longer clears tax/inventory flags

- **Date:** 2026-07-31
- **Task:** ut-docs#178
- **Branch:** `fix/settings-save-currency-only-clears-tax-flags`
- **Author:** pipeline Dev step (Sonnet 5)
- **Independent reviewer:** general-purpose subagent on **Opus** (different model, per standing practice)

## What shipped

`internal/pages/settings_page.go`'s `POST /api/settings/save` handler no
longer unconditionally sets `TaxInclusive`/`AllowNegativeInventory` from
`r.Form.Get(...) == "on"`. Its only caller — the currency card in
`web/ui/pages/settings.html` — posts nothing but `currency`, so every
currency change silently zeroed both flags, even for a shop that had
explicitly enabled tax-inclusive pricing or negative-inventory allowance.
The fix removes the two unconditional lines; the fields remain settable
through the pre-existing `/api/settings/upsert` generic key/value path
(`store.tax_inclusive`, `pos.allow_negative_inventory`), already wired to
the settings page's raw key/value editor. `taxRatePct`'s existing presence
guard was left untouched — confirmed not part of the bug (it already only
applied when the form key was present).

`internal/pages/settings_page_test.go`: added
`TestSaveSettingsCurrencyOnlyDoesNotClearTaxOrInventoryFlags` (seeds both
flags true, persisted and in-memory, then POSTs only `currency` through the
real HTTP mux, asserting both survive in state *and* in the settings store).
Updated `TestDisplayAndStoreSettings` to match the new `/save` contract
(currency/country/taxRatePct only) and to exercise `store.tax_inclusive` /
`pos.allow_negative_inventory` via `/upsert` instead, so flag-setting
coverage isn't lost, just moved to the correct endpoint.

## TDD evidence (independently re-verified twice — Tester, then Reviewer)

Both the pipeline's own Tester step and the independent Opus reviewer
isolated `internal/pages/settings_page.go`'s diff (`git checkout --` on just
that file), re-ran the new test, and got a genuine assertion failure:
```
settings_page_test.go:223: currency-only save silently cleared TaxInclusive
```
— not a compile error. Re-applying the fix made it pass again. Repeated a
third time after strengthening the test post-review (see finding #4 below)
with the same result.

## Verified beyond automated tests

- **Real running server.** Started the actual headless binary (`go run .`,
  `UT_AUTH=off`), seeded both flags true via `/api/settings/upsert`,
  confirmed them `true` in the settings page's raw key/value table, POSTed
  a currency-only form to `/api/settings/save` exactly as the shipped form
  does, and confirmed via the same raw table that both flags were still
  `true` and currency had changed to GBP (£, marked `selected` in the
  dropdown). Repeated the same sequence against the **unfixed** handler in
  a second instance and watched both flags flip to `false` — reproducing
  the reported failure scenario live, not just in a unit test.
- Full `go build ./...`, `go vet ./...`, `go test ./...`, both CI guards
  (`guard-data-access.sh`, `guard-i18n.sh`) green. `-race` clean on the
  affected package. `gofmt -l` on the touched files clean.
- One pre-existing, unrelated failure confirmed both before and after this
  change, on a clean stash of unmodified `main`:
  `internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure` — this
  container runs as root, so a read-only-directory test can't actually
  block root's writes. Not caused by, and out of scope for, this fix.
- No server/binary processes or temp data directories left running after
  either the Tester's or Reviewer's manual runs.

## Review findings

| # | Severity | Finding | Outcome |
|---|----------|---------|---------|
| 1 | worth noting | Neither `/api/settings/save` nor `/api/settings/upsert` calls `isManagerOrAuthOff`, unlike most sibling settings endpoints in the same file — any signed-in cashier can currently write these flags (and the raw key/value table renders outside the `.isManager` block in the template). Confirmed **pre-existing and symmetric** — both endpoints already lacked the check before this fix; the fix does not change the authorization posture, it only moves the sanctioned path from one ungated endpoint to another ungated endpoint. | **Deferred → ut-docs#179** (new Backlog card; matching an already-established in-file pattern, no design/business decision needed, just out of scope for a currency-field regression fix) |
| 2 | nitpick | The removed-lines comment said the flags are "manager-editable via `/api/settings/upsert`" — overstates the current access control given finding #1. | **Fixed** — reworded to state the mechanism without the incorrect authorization claim |
| 3 | nitpick | The `taxRatePct` comment's stated reason for keeping its guard ("reachable via `/upsert`'s `KeyTaxRate`") was logically wrong — `/upsert`'s `KeyTaxRate` branch is a different handler and doesn't make `/save`'s `taxRatePct` branch reachable; no shipped UI posts it to `/save` at all. | **Fixed** — reworded to say it's exercised by the test, not reachable from shipped UI, rather than implying reachability it doesn't have |
| 4 | nitpick | Regression test only asserted in-memory `CurrentState()`; a future regression that broke persistence without breaking in-memory state would slip through. | **Fixed** — added `d.Settings.Get` assertions for both keys, and seeded the pre-fix state through `common.SaveState` (persisted, not just in-memory) so the test matches a real shop boot |
| 5 | nitpick | `/api/setup`'s `tax_inclusive` field defaults to `true` on absence while `/save` now means "leave alone" — an asymmetry that's harmless today (the setup wizard's hidden Alpine-bound field always posts a value) but worth a maintainer's awareness if that binding ever breaks. | Accepted, documented here — no code change; `/api/setup` is a separate handler, unaffected by and out of scope for this fix |

Also checked clean by the independent reviewer: no other caller of `/save`
anywhere in the repo; no orphaned UI control for the removed fields; no
`os.MkdirAll`/file-write or cwd-relative-path bug classes (this change
writes no files); `TestDisplayAndStoreSettings` still exercises real state
transitions in both directions, not a tautology; no data races (`-race`
clean); no real shop/client name or secret-shaped literal in any changed
line.

## Verdict

**Safe to merge.** Nothing blocking. One real, pre-existing (not
introduced by this change) authorization gap found and carded as
ut-docs#179; all in-scope nitpicks fixed in-branch.
