# Code review: fiscal.pending_sign_retries excluded from admin sync (ut-docs#844)

**Date:** 2026-08-20
**Card:** ut-docs#844 (found during independent review of ut-docs#839/ADR-0056)
**Complexity:** easy — build: Sonnet (inline), review: Sonnet (fresh-context subagent, isolated worktree)

## What shipped

`fiscal.pending_sign_retries` (`common.KeyPendingFiscalSignRetries`) — the
pre-1.4.0 background fiscal-signing retry queue, cleared once at boot by
`pages.dropStaleFiscalSignRetryQueue` (ADR-0056, ut-docs#839) — was missing
from `internal/data.PerTillSettingPrefixes`, so it was treated as shared,
replicated admin-settings state. During a mixed-version rollout, a
pre-1.4.0 primary till could re-seed its stale retry queue onto an
already-migrated 1.4.0 replica on a later sync, after that replica's
one-time boot migration had already cleared it (the migration runs once at
boot, not on every sync).

Confirmed no functional/compliance impact today (nothing on the 1.4.0
build reads the key in either direction — a re-synced value would be
inert) — this closes a housekeeping/cleanliness gap, and is what actually
makes `dropStaleFiscalSignRetryQueue`'s own "must not linger" doc-comment
claim true end-to-end rather than true-at-boot-only.

Fix: added the key as an exact-match entry in `PerTillSettingPrefixes`
(`internal/data/sync_admin_repo.go`), exported as a named
`FiscalPendingSignRetriesSettingsKey` const (not a bare literal) following
the existing `StoreCountrySettingsKey` pattern in the same file
(`reset_archive_repo.go`) — `internal/pages/common` already imports
`internal/data`, so the reverse import would cycle; the const exists so a
companion test in `internal/pages/common` can assert the two packages'
copies of the key never drift apart, instead of each just trusting its own
copy. Cross-referenced doc comments between `PerTillSettingPrefixes` and
`dropStaleFiscalSignRetryQueue`.

## Independent review (Sonnet, fresh-context subagent, isolated worktree)

Ran build/vet/targeted tests/guard script — all green. Independently
reverted the one-line fix and re-ran the new regression test, confirmed it
fails with the expected "leaked into the admin dump" error at the
`DumpAdmin` assertion; restored and confirmed it passes again. Confirmed
the string literal used was an exact match against
`common.KeyPendingFiscalSignRetries`'s real value (not a typo-masked
no-op). Also checked and cleared: no raw SQL outside the data layer, no
money type involved, no user-facing strings/UI touched (guard-i18n and a
`git diff --stat` both confirm zero `web/` files in the diff), no
filesystem I/O, offline-first unaffected, and the separate full-DB-
snapshot join path (`internal/db/replica.go`, which bypasses
`PerTillSettingPrefixes` entirely) is not a gap because
`dropStaleFiscalSignRetryQueue` runs unconditionally on every boot and a
join always requires a restart first.

**One SHOULD-FIX**, taken before merge: the initial fix used a bare
hardcoded string literal in `PerTillSettingPrefixes` rather than following
this codebase's established drift-guard pattern for exactly this
situation (`data.StoreCountrySettingsKey` + `TestStoreCountrySettingsKeyMatchesCommon`,
used because `internal/data` cannot import `internal/pages/common` without
cycling). Without it, if `common.KeyPendingFiscalSignRetries` were ever
renamed, the new regression test (which hardcodes the same string
independently) would keep passing while the real protection silently
broke — reintroducing #844's own bug with nothing to catch it. Fixed:
exported `data.FiscalPendingSignRetriesSettingsKey` and added
`TestFiscalPendingSignRetriesSettingsKeyMatchesCommon` in
`internal/pages/common/state_test.go`, mirroring the existing pattern
exactly.

One NIT noted, not fixed (cosmetic only, already well-explained in the
surrounding doc comment): the new `PerTillSettingPrefixes` entry is a full
key, not a `.`/`_`-terminated prefix family like the other four — a future
skim of the list could misread it as intended to match a
`fiscal.pending_sign_retries*` family. `strings.HasPrefix` on an
equal-length string is a true equality test, so this is correct as
written.

## What was verified beyond automated tests

- **TDD re-verified independently**, twice: once by the implementer (this
  same session, inline) and once by the independent reviewer in its own
  isolated worktree — both reverted the fix, confirmed the real pre-fix
  failure (`fiscal.pending_sign_retries leaked into the admin dump`), then
  restored and confirmed the fix passes.
- Full `go test ./...` green after the SHOULD-FIX, not just the targeted
  packages.
- All 4 required guard scripts green (`guard-data-access.sh`,
  `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`), plus
  `guard-i18n.sh` (no user-facing strings touched, confirmed rather than
  assumed).

## Explicitly out of scope (per the card's own text)

None — the card's acceptance criteria (exclude the key from sync
replication, add a regression test, update the doc-comment claim if it
becomes actually enforced) are fully met; no follow-up card needed.

## Safe-to-merge verdict

**Safe to merge.** No open blockers. Full gate green.
