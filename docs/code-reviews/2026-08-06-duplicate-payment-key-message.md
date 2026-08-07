# 2026-08-06 — Duplicate payment-entry Key: clean error instead of raw SQLite text

**Card:** universaltill/ut-docs#363
**Repo / files:** `universal-till` — `internal/plugins/manifest.go`,
`internal/plugins/payment_key_validation_test.go`
**Model routing:** `complexity:easy` — built inline (Sonnet), reviewed by a
fresh-context Sonnet subagent (no prior conversation history).

## What changed

`validatePaymentEntryKeys` already rejects two payment entries sharing one
`Label` within a manifest with a clean `fmt.Errorf` (ut-docs#168). It had no
equivalent check for a duplicate `Key`. Unlike the label case, this was never
a silent-data-loss bug — `plugin_entries` has a real
`UNIQUE(plugin_id, key)` constraint, so `PersistManifest` already failed and
rolled back the whole transaction — but the caller saw the raw SQLite
message (`UNIQUE constraint failed: plugin_entries.plugin_id,
plugin_entries.key`) instead of a message naming the key, unlike every other
check in the function.

Added a `seenKeys` map mirroring the existing `seenLabels` one: a duplicate
`Key` within the same manifest is now rejected before the DB round-trip,
with `payment entry key %q is used by more than one entry in this
manifest — pick distinct keys`.

Message-quality fix only — no change to what gets accepted or rejected.

## TDD

`TestPersistManifest_RejectsDuplicateKeysWithinSameManifest` written first,
confirmed failing pre-fix against the raw SQLite text
(`internal/plugins`, `go test -run TestPersistManifest_RejectsDuplicateKeysWithinSameManifest -v`),
then passing post-fix. Also asserts the raw `"UNIQUE constraint failed"`
text no longer leaks.

## Independent review (fresh-context Sonnet subagent)

**Verdict: safe to merge as-is. Zero blockers, one non-blocking nit.**

- Checked ordering: the new check runs after the existing key format checks
  (empty/whitespace/`:`) and before the label checks, mirroring where the
  label-duplicate check sits relative to the label-empty check. Fires before
  the DB round-trip (`FindPaymentKeyConflicts`/`FindPaymentNameConflicts`).
- Case sensitivity: exact-match Go map, consistent with `plugin_entries`'
  plain (non-`COLLATE NOCASE`) `UNIQUE(plugin_id, key)` column.
- Independently re-verified the TDD claim by reverting only the production
  fix (keeping the new test) and re-running it — reproduced the exact raw
  SQLite failure from the issue, confirming the test is load-bearing.
  Restored the fix; `go test ./internal/plugins/...` green, `gofmt -l`
  clean, `go build ./...` / `go vet ./...` clean, `guard-data-access.sh`
  clean.
- `validatePaymentEntryKeys` is shared by `PersistManifest` and
  `Rollback` (`internal/plugins/rollback.go:131`), so the legacy-manifest
  rollback path gets the same clean message for free.
- Scope: touches only the two intended files, no scope creep.
- i18n / manual: confirmed genuinely consistent with the raw-`err.Error()`
  pattern already used at every `http.Error(w, err.Error(), ...)` call site
  in `internal/pages/plugin_api.go` (separately tracked as a wider gap in
  ut-docs#316). This error is also never routed through that HTTP path in
  practice — `PersistManifest` is called from plugin install/import flows
  (`install.go`, `installer_marketplace.go`, `importer.go`), surfaced to a
  plugin author/vendor, not a shop owner via a rendered template — so no
  i18n key and no manual topic are needed.
- **Nit (not fixed, pre-existing pattern):** the `n != 0` /
  "transaction must roll back" assertion in both this test and the sibling
  `TestPersistManifest_RejectsDuplicateLabelsWithinSameManifest` doesn't
  actually exercise a rollback — `validatePaymentEntryKeys` runs as step 0,
  before any row is written, so the early return means `n==0` trivially,
  not because `tx.Rollback()` undid a partial write. The assertion still
  correctly proves no partial state leaked; it just isn't proof of what its
  message claims. Consistency with an already-merged, already-reviewed
  precedent, not a new defect — left as-is; noted here for the record
  rather than opening a new card over a one-line assertion-message wording.

## Verification beyond the unit test

- `go build ./...`, full `go test ./...` (once, after all edits) — all
  packages pass except `internal/issuereport`'s
  `TestSaveCleansUpDirectoryOnWriteFailure`, which is a pre-existing,
  unrelated environment failure: this container runs as root, so a
  `0o500`-permission directory doesn't actually block a write (root bypasses
  permission bits), and the test relies on that OS-level enforcement.
  Confirmed pre-existing by stashing this change and re-running the same
  test against unmodified `main` — same failure. Not touched by this diff.
- `bash scripts/ci/guard-data-access.sh` — clean.
- `gofmt -l` — clean.

## Not done (deliberately out of scope)

- No i18n key / manual topic — not user-facing template text (see above).
- The pre-existing rollback-assertion nit above — cosmetic, not this card's
  problem, and shared with already-merged code.
