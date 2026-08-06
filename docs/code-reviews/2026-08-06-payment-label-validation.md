# 2026-08-06 — install-time payment label validation gaps (ut-docs#168)

Card: [ut-docs#168](https://github.com/universaltill/ut-docs/issues/168) (p3, easy)
Branch: `fix/168-payment-label-validation`

## What shipped

`validatePaymentEntryKeys` (`internal/plugins/manifest.go`) already
validated payment-entry `Key` (non-empty, whitespace-clean, no `:`) and
checked both `Key` and `Label` for collisions against *other* plugins'
rows (`FindPaymentKeyConflicts`/`FindPaymentNameConflicts`, ADR-0031 /
ut-docs#16). Two gaps remained, found by the independent review of
ut-docs#16 (universal-till PR #123):

- **Duplicate labels within a single manifest** were never checked
  against each other — only against other plugins' rows. A manifest with
  two payment entries sharing one label installed cleanly; only one
  materialized into `payment_methods` at sync time, and the other
  silently vanished (no error at install, no warning at sync, since it's
  an intra-plugin collision rather than the cross-plugin kind the sync's
  warning path detects).
- **Empty `Label` was never validated**, unlike `Key`. An entry with
  `Label: ""` installed and synced to a blank-named tender
  (`payment_methods.name = ""`); a second plugin doing the same then lost
  one entry to the empty-label collision.

Fix: `validatePaymentEntryKeys` now tracks labels seen so far in the
current manifest (`seenLabels map[string]bool`) and rejects a repeat, and
rejects an empty-or-whitespace-only label — both checks sit inside the
existing `e.Type != "payment"` guard, both inside the same install
transaction as the pre-existing key checks, so a rejected manifest still
rolls back cleanly (no half-installed `plugins` row).

Three new regression tests in `internal/plugins/payment_key_validation_test.go`,
written test-first and confirmed failing pre-fix:
`TestPersistManifest_RejectsDuplicateLabelsWithinSameManifest`,
`TestPersistManifest_RejectsEmptyPaymentLabel`,
`TestPersistManifest_RejectsWhitespaceOnlyPaymentLabel`.

## Independent review (fresh-context Sonnet — easy-tier card)

Ran build/vet/`go test ./internal/plugins/...`/the data-access guard
itself, and independently re-verified the TDD claim: reverted just the
`manifest.go` fix (kept the new tests), confirmed all three fail with the
claimed errors, restored the fix, confirmed all three pass again.

**Verdict: safe to merge.** No blocking or non-blocking findings against
the diff. Confirmed:

- Both new checks sit inside the existing payment-type guard, so
  non-payment entries with an empty `Label` are untouched.
- The duplicate-label check is exact-match, case-sensitive — matches
  `payment_methods.name`'s plain `UNIQUE` column (no `COLLATE NOCASE` in
  `001_init.sql`), so no case-sensitivity mismatch with the DB constraint
  it stands in front of.
- `Rollback` calls the same `validatePaymentEntryKeys`, so a legacy
  on-disk manifest restoring a duplicate/empty label is covered too —
  confirmed by reading the call site; `TestRollback_RejectsCollidingPaymentKeys`
  still passes.
- Self-upgrade/reinstall unaffected — `seenLabels` is function-local per
  call, so re-persisting the same manifest across separate installs still
  passes.
- i18n/user-manual: N/A. These are `fmt.Errorf` values surfaced as plain
  Go errors via install-status/logs to a plugin author/vendor, never
  template-rendered — matches the precedent of the sibling key/label
  collision errors this function already returns (reviewed in
  `docs/code-reviews/2026-07-30-plugin-payment-key-hijack.md` and
  `2026-07-31-plugin-payment-method-followups.md`), which also carry no
  i18n key and no manual topic. No shop owner/manager ever sees this
  text.
- No `os.MkdirAll`-applicable file writes, no cwd-relative paths — N/A,
  no file I/O in the diff.
- No real client/shop name or secret-shaped literal in test data.

**Adjacent gap noted, not a blocker, filed separately:** duplicate
*keys* (not labels) within one manifest aren't given a friendly
pre-check the way labels now are — but this doesn't silently lose data
the way the label gap did: `plugin_entries` has
`UNIQUE (plugin_id, key)`, so a manifest with two payment entries
sharing one `Key` already fails cleanly inside the same transaction
(rolls back), just with a raw SQLite constraint message instead of a
clean `fmt.Errorf`. `plugin_entries.label` has no equivalent DB
constraint, which is exactly why the label gap could silently lose an
entry and the key gap can't. Tracked as
[ut-docs#363](https://github.com/universaltill/ut-docs/issues/363)
(friendlier error message only, not a correctness bug).

## Verification

- `go build ./...`, `go vet ./...` — clean.
- `go test ./internal/plugins/...` — all pass (new + existing).
- `go test ./... -race` — green, except the same pre-existing,
  environmental `internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure`
  already noted in PR universaltill#186's own review record (the
  package is untouched by this diff; the container runs as uid 0, so a
  `0o500` directory doesn't actually block a write there).
- `bash scripts/ci/guard-data-access.sh` — clean.
- `bash scripts/ci/guard-i18n.sh` — clean (829 keys, all locales match).

Closes universaltill/ut-docs#168
