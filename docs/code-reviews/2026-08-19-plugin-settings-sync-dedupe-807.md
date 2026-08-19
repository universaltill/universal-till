# Code review — plugin_settings sync-apply dedupe + sync chip honesty (ut-docs#807)

Ticket: universaltill/ut-docs#807
Branch: `fix/807-plugin-settings-sync-dedupe`
Date: 2026-08-19

## What the change does

`ux_plugin_settings_global` (migration 053, ut-docs#787) added a real unique
index for `plugin_settings` global rows. That surfaced a gap:
`applyPluginSettings` (`internal/data/sync_admin_repo.go`, the LAN-sync
apply path) had no dedupe of its own, so a stale pre-migration-053 primary
sending two global rows for the same `(plugin_id, key)` in one bundle now
aborted the *entire* admin-bundle apply — not just plugin_settings, but
catalog, users, tax codes, payment methods, the till roster too — on every
pull, forever, until the primary was fixed. Separately, `syncPullTick`
(`internal/pages/sync_admin.go`) set `sync.last_contact_at` — the sync
chip's freshness signal — *before* calling `ApplyAdmin`, so a failed apply
still left the chip reporting healthy contact even though
`sync.pull_version` never advanced.

Two fixes, per the ticket's own acceptance criteria:

1. **Defensive dedupe** (`dedupeGlobalPluginSettings` +
   `pluginSettingWins`, `internal/data/sync_admin_repo.go`): before ever
   inserting, collapse scope='global' bundle rows to one winner per
   `(plugin_id, key)`, mirroring migration 052's own tiebreak
   (`updated_at DESC, id DESC`). Turns a shop-wide sync outage into a
   self-healing collapse — a loud failure becomes a logged, contained one.
2. **`sync.last_contact_at` ordering fix** (`internal/pages/sync_admin.go`,
   `syncPullTick`): the write moves to after the outcome is known (bundle
   unchanged, or apply succeeded) instead of unconditionally right after
   the HTTP round-trip, so the chip stops lying "healthy" on a tick whose
   apply failed.

## What this session did

1. Implemented the original diff (Dev, inline — Sonnet, complexity:medium
   per the card's label): the dedupe + tiebreak in
   `internal/data/sync_admin_repo.go`, the `last_contact_at` reorder in
   `internal/pages/sync_admin.go`, TDD-first — each new test written and
   confirmed to fail against the pre-fix code with the real, on-topic
   error before the fix landed:
   - `TestAdminSyncSharedPluginSettingsDedupesDuplicateGlobalRowsInBundle`
     / `TestAdminSyncSharedPluginSettingsDedupeTiebreaksOnIDWhenUpdatedAtTies`
     (`internal/data/sync_admin_repo_test.go`) replace the old
     `...RejectsDuplicateGlobalRowsInBundle` test, whose expectation the
     dedupe deliberately inverts (apply now succeeds instead of erroring).
     The raw index's own DB-level rejection (independent of the sync-apply
     path) moved to a new low-level test,
     `TestUxPluginSettingsGlobalRejectsDuplicateRow`
     (`internal/db/plugin_settings_dedupe_migration_test.go`), so the
     schema-level backstop itself stays pinned for any writer other than
     the now-defensive sync-apply path.
   - `TestSyncPullTick_ApplyFailureDoesNotSetLastContactAt`
     (`internal/pages/sync_admin_test.go`) uses a deliberately
     dedupe-unrelated failure (two `items` rows sharing a SKU) so it
     exercises the ordering fix in isolation, not a scenario the dedupe
     itself would already have prevented.
2. Independent review (Opus subagent, isolated worktree, per the
   medium-complexity routing table) read the diff and surrounding code in
   full, ran `go build`/`go vet`/full `go test ./...`/all data-access,
   kiosk-engine, plugin-menu-read, i18n and help-topics guards, and
   independently re-verified both TDD claims by reverting each fix,
   re-running its test, confirming the real on-topic failure, then
   restoring and confirming green again. It additionally wrote and ran a
   throwaway cross-check (since deleted) comparing the Go tiebreak against
   migration 052's actual SQL across 5 fixtures — all agreed.

   **Verdict: safe to merge, no blocking issues.** Six non-blocking
   findings; four addressed in this session, two logged as deliberately
   deferred:
   - **Fixed — silent drop had no log signal.** `dedupeGlobalPluginSettings`
     now emits a `logging.L().Warnf` naming the dropped row's id,
     plugin_id and key (and the id kept) whenever a duplicate is
     collapsed — converting a loud failure into a *logged*, not silent,
     one. Mirrors the existing `deleteMissing` Warnf precedent in the same
     file.
   - **Fixed — the two dedupe tests didn't actually pin the tiebreak
     *direction*.** Both fixtures originally listed the intended winner
     last, so a degenerate "last row always wins" `pluginSettingWins`
     (ignoring `updated_at`/`id` entirely) passed both. Reordered
     `TestAdminSyncSharedPluginSettingsDedupesDuplicateGlobalRowsInBundle`'s
     bundle so its winner is listed *first* — together with the tiebreak
     test's winner-listed-*last* ordering, the pair now catches both an
     "always take last" and an "always take first" degenerate
     implementation. Verified directly: temporarily mutated
     `pluginSettingWins` to `return true` (unconditional) and separately
     to `return false` (unconditional) — each mutant broke exactly one of
     the two tests, confirming the pair now pins the real comparison
     rather than an artifact of row order. Reverted after confirming.
   - **Fixed — the `last_contact_at` comment overstated its own effect.**
     `sync_sales.go` also refreshes `sync.last_contact_at` on a successful
     journal push, so a replica whose sales are pushing normally but whose
     admin pull is stuck can still show the chip as fresh within 30s —
     pre-existing, not fixed by this change. Softened the inline comment
     in `syncPullTick` to say "a strict improvement, not a complete fix"
     rather than implying this is the chip's only write path.
   - **Deferred, logged only** — mixed `updated_at` write formats
     (`datetime('now')` vs RFC3339) in `internal/data/plugin_repo.go` mean
     the recency tiebreak (both here and in migration 052 itself) can pick
     the "wrong" row within the same calendar day. Pre-existing data
     hygiene issue affecting 052 identically, not introduced or worsened
     by this change — real, but a separate card's scope, not filed here to
     avoid speculative backlog noise for a low-severity same-day-only
     effect.
   - **Deferred, logged only** — `web/help/en/multitill.md`'s description
     of the sync chip's amber state is now slightly narrower than reality
     (amber can also mean "reachable primary, unappliable bundle", not
     just "not heard from"). A translated, multi-locale prose edit needs
     the homelab's self-hosted translation pipeline
     (`reference/translation.md`), which this cloud session cannot reach —
     not attempting a partial, English-only edit that would leave other
     locales silently inconsistent with it. Logged here rather than filed
     as its own card: low severity (a chip-tooltip nuance, not a
     functional claim), and the reviewer itself framed it as optional
     ("worth a half-sentence edit") rather than a defect.
   - Noted, no action needed — the review record itself (this file) is
     what closed finding #6 ("no review record exists yet on this
     branch").

## Verified beyond automated checks

- Re-ran the full gate after applying the review's fixes (not just the
  specific cases each finding named): `go build ./...`, `go vet ./...`,
  `go test ./internal/data/... ./internal/pages/... ./internal/db/...`
  (data 45.4s, pages 146.4s, db cached), full `go test ./...`, and all six
  guard scripts (`guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-i18n.sh`, `guard-help-topics.sh`,
  `guard-compliance-claims.sh`) — all green. `gofmt -l` on every file this
  change touches — clean.
- Independently re-verified the mutation-resistance of the two dedupe
  tests myself (not just trusting the reviewer's report) by re-running the
  same `pluginSettingWins` mutant swap after applying the reviewer's own
  suggested fix — confirmed both mutants (`return true`, `return false`)
  now each break exactly one test, restored the real implementation, and
  confirmed both tests pass again.
- Cross-checked that this diff is backend/sync-logic only: zero `web/`
  changes, zero new user-facing strings, `guard-i18n.sh` and
  `guard-help-topics.sh` both pass — so no locale-key or manual-topic
  follow-up is owed by this change itself (the two deferred findings above
  are pre-existing/adjacent, not caused by this diff).
- No SQL added outside `internal/data`/`internal/db` (repository-pattern
  rule) — confirmed by `guard-data-access.sh` and by reading the diff: the
  only new SQL execution is `dedupeGlobalPluginSettings`'s pure in-memory
  logic (no SQL at all) and the existing per-plugin `DELETE`/upsert calls,
  unchanged in shape.
- No real client/shop name used as test/demo data in any new test
  (`com.ut.dup`, `com.ut.tie`, `com.t.ux` are the only new fixture ids).

## Outcome

Safe to merge. Two production files changed
(`internal/data/sync_admin_repo.go`, `internal/pages/sync_admin.go`), test
coverage added/relocated across three files
(`internal/data/sync_admin_repo_test.go`,
`internal/db/plugin_settings_dedupe_migration_test.go`,
`internal/pages/sync_admin_test.go`). All six of the independent review's
findings were non-blocking; four were fixed in this session and two are
explicitly logged as deliberately deferred (data-hygiene root cause and a
translation-dependent doc nuance, both pre-existing and out of this
change's scope).
