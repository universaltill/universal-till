# Code review: Payment key/name install-conflict errors misclassified as retryable

**Ticket:** universaltill/ut-docs#169
**Date:** 2026-08-08
**Repo/branch:** `universal-till` / `fix/payment-conflict-install-error-classification`
**Reviewer:** independent Opus subagent (complexity:medium tier), isolated worktree

## What shipped

Two related gaps in how a payment-entry key/label collision at plugin
install time reaches the operator:

1. `ClassifyInstallError` (`internal/plugins/install_status.go`) had no
   case recognizing a payment key/label conflict — both fell to the
   generic `default` branch (`plugins.install.error.retryable`, "Install
   failed. You can retry."). That's actively wrong: a payment key/label
   collision is a **permanent** failure — retrying the same manifest
   always fails the same way — until the plugin author picks a different
   key/label.
2. `FindPaymentNameConflicts`'s error construction in
   `validatePaymentEntryKeys` (`internal/plugins/manifest.go`) only had a
   2-way branch (no owner / owner), unlike `FindPaymentKeyConflicts`'s
   3-way switch (no owner / owner not installed / owner installed) — so a
   label collision with an **uninstalled** plugin's retained tender row
   reported as owned by a plugin the operator can't see or remove,
   instead of the correct "no longer installed" framing the key-conflict
   path already gave.

### Fix

- `manifest.go`: mirrored the key-conflict 3-way switch into the
  name-conflict branch, byte-for-byte wording pattern (`belongs to plugin
  %s, which is no longer installed — its tender row is retained for
  sales history; reinstall it or pick a different label`).
- `install_status.go`: added a `ClassifyInstallError` case matching the
  six real collision message shapes (`"payment entry key"`/`"payment
  entry label"` + `"collides with"`/`"belongs to plugin"`/`"is already
  provided by plugin"`/`"is already used by plugin"`), returning
  `Retryable: false` and a new message key. Deliberately scoped to
  exclude the separate within-manifest-duplicate and malformed-key
  validation errors (different bug class, out of scope for this ticket)
  — verified empirically, not just by inspection (see below).
- New locale key `plugins.install.error.payment_conflict` added to all
  four locale files (en/ar/fa/tr) — real translations, not copies of
  English.
- Regression tests, written test-first (TDD): `TestClassifyInstallError`
  gained six payment-conflict cases plus the production-wrapped form;
  `TestPersistManifest_NameReservedByUninstalledPluginSaysSo` mirrors the
  existing key-conflict equivalent.

## Independent review (round 1)

An independent Opus subagent, isolated in its own git worktree, reviewed
the diff adversarially:

- Ran `go build`, `go vet`, `go test ./internal/plugins/...`, and all
  three guards (`guard-data-access.sh`, `guard-i18n.sh`,
  `guard-help-topics.sh`) itself — all green.
- **Independently re-verified the TDD claim**: reverted just the two
  fix files (kept the new tests), re-ran the affected tests, confirmed
  they fail with the exact expected error text (including the real
  pre-fix bug: an uninstalled-owner label conflict reporting "is already
  used by plugin com.gone.namepay" instead of "no longer installed"),
  then restored the fix and confirmed green again.
- **Verified the classifier's scoping empirically**, not by eye: wrote a
  throwaway probe enumerating all twelve real message shapes
  `validatePaymentEntryKeys` can produce (six true collisions, six
  validation/duplicate errors) and confirmed the new case matches
  exactly the six collision messages and none of the six others — zero
  false positives, zero misses, and no widening/narrowing of the
  intended scope.
- Confirmed against the two recurring bug classes this pipeline has
  been burned by before (missing `os.MkdirAll`, cwd-relative path
  instead of `paths.Data`) — neither applies; the diff touches no file
  I/O.
- Confirmed no SQL outside `internal/data`/`internal/db` (only a test
  fixture `INSERT`, matching an existing precedent 20 lines above it),
  no money math, no plugin-loading/verification path touched.
- Confirmed no manual-doc topic needs updating — `web/help/en/plugins.md`
  never enumerates install-failure states, so this text-only badge
  change makes nothing in it stale.
- Confirmed all locale files parse, all four have the identical key
  count, and the new key sits correctly (alphabetically, among sibling
  `plugins.install.error.*` keys) in all four.
- Confirmed all test/fixture data is generic (`com.other.pay`,
  `com.gone.pay`, `Ghost Tender`, `Shared Label`, …) — no real shop/client
  name.

### Findings (both low severity, both folded in — no blocker-class issue, so no second review round)

1. **Test coverage gap**: the six original `ClassifyInstallError` test
   cases used the bare `validatePaymentEntryKeys` error text, but
   production always wraps it (`persist plugin manifest: %w` in
   `installer_marketplace.go`) before it reaches the classifier — a
   coincidental future wording change to the earlier `"manifest
   validation"` case could have made the payment-conflict case
   unreachable in production while every existing test still passed.
   **Fixed**: added a test case using the real wrapped-error shape.
2. **Locale coverage gap**: `guard-i18n.sh`'s static template scan can't
   see `{{ T .StatusMessageKey }}` (a dynamic key) in
   `plugins_store.html`, so a missed locale entry for a new
   `ClassifyInstallError` key would have shipped with CI green and shown
   the raw key string in the UI. **Fixed**: added
   `TestClassifyInstallError_KeysResolveInLocales`, which loads the real
   embedded locale files (`config.NewI18nFS(locales.FS, "en")`) and
   asserts every key `ClassifyInstallError` can return actually
   translates.

Two informational, no-action items: `InstallFailure.Message` (the Go
fallback string) differs slightly in wording from the en.json UI string —
consistent with the other five existing cases, and `Message` is read at
zero call sites (only `MessageKey`/`Retryable` are used), so this is
harmless. A contrived manifest whose payment key/label is literally the
string `"collides with"` would also classify as a conflict via the
duplicate-within-manifest path — theoretical, and arguably still correct
wording for that edge case.

## Verification performed (this session, after the fix)

- `go build ./...`, `go vet ./...` — clean.
- `go test ./...` — all packages pass except the pre-existing, unrelated
  `TestSaveCleansUpDirectoryOnWriteFailure` (`internal/issuereport`),
  which fails under a root-run sandbox (`chmod 0500` doesn't block root)
  — confirmed present on `origin/main` before this change too (tracked
  separately as ut-docs#415).
- `bash scripts/ci/guard-data-access.sh`, `guard-i18n.sh`,
  `guard-help-topics.sh` — all green.
- Both reviewer-found gaps closed with real regression tests, not just
  asserted fixed.

## Scope

Backend error-classification/messaging only — no new UI surface (the
message renders through the same `<span class="tag">` badge five sibling
`ClassifyInstallError` keys already use), no money/data-access/
offline-first/plugin-signing implications, no manual-doc update needed.

## Outcome

Independent review found two low-severity test-coverage gaps, both fixed
and re-verified in this same round. No blocker-class issue (money/tax,
data loss, security) — no second review round.

Safe to merge.
