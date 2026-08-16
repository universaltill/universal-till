# Review: UpsertPluginSettingScoped duplicate-row race (ut-docs#785)

**Card:** universaltill/ut-docs#785 — "UpsertPluginSettingScoped's
non-transactional upsert can create duplicate global plugin_settings rows
(scope_id NULL unique-index gap)"
**Complexity:** medium. **Build:** Sonnet (inline). **Review:** Opus
(fresh-context subagent, independent of the build, isolated worktree).

## What was asked

`PluginRepo.UpsertPluginSettingScoped` (`internal/data/plugin_repo.go`) did
an UPDATE, then — if 0 rows were affected — an INSERT, as two separate,
non-transactional statements. `plugin_settings`' `UNIQUE(plugin_id, key,
scope, scope_id)` index doesn't block duplicate `scope='global'` rows
because `scope_id` is `NULL` for global rows and SQLite treats `NULL`s as
distinct in a unique index. Two near-simultaneous (or unlucky sequential)
callers could each see 0 rows in their UPDATE and both fall through to
INSERT, leaving two rows for the same `(plugin_id, key, global)` triple —
pre-existing behaviour, found during ut-docs#668's review, not introduced
by that diff.

## Fix

Wrapped the update-then-insert in its own transaction (`r.db.BeginTx`),
mirroring the neighbouring `MergeAdditiveJSONMapSetting`'s existing
serialization pattern: the DSN's `_txlock=immediate` (ut-docs#311) makes
`BeginTx` take the write lock at `BEGIN`, so a second caller for the same
`(plugin_id, key, scope)` can't start its own UPDATE until the first has
committed. No schema change — the only non-test caller
(`internal/pages/plugin_settings_page.go:113`) holds no transaction of its
own, so there's no nesting hazard.

## TDD

Regression test written first, confirmed failing against the pre-fix code,
then the fix applied and the test confirmed passing (`git diff` isolation,
not just re-running after the fact).

**The first version of the concurrency test was weak — the independent
review caught this, and it's worth recording why.** A single batch of N
concurrent callers only gets *one* shot at the race: the vulnerable window
exists only while the row is absent, and once any caller's INSERT lands,
every later UPDATE in that batch matches a row. More goroutines don't help.
Measured: a 20-goroutine batch reproduced the duplicate in under 20% of
runs against the pre-fix code, and **zero out of 50 runs under `-race`** —
a test that would pass on buggy code most of the time it's actually run in
CI (`.github/workflows/ci.yml` runs plain `go test`, no `-race`).

Fixed by repeating the race across independent rounds (25 rounds × 8
goroutines, each round deleting the row first so it gets its own fresh
"absent" window, with a start-barrier channel so all 8 fire together).
Verified independently — not just re-trusting the reviewer's numbers —
by literally reverting the production fix and running the strengthened
test: **failed 3/3 runs** against pre-fix code (caught within 1–7 rounds
each time), then restored the fix and confirmed **5/5 clean passes with
`-race`**.

## Independent review (Opus, isolated worktree)

Findings, triaged:

1. **Concurrency test had ~19% detection power, 0% under `-race`**
   (real-but-non-blocking → fixed before merge, see TDD section above).
2. **Fix mechanism verified correct** — re-ran the reviewer's own
   100%-reliable probe against both pre-fix (100% failure) and fixed
   (100% pass) code; confirmed no nesting hazard by checking every caller
   of `UpsertPluginSettingScoped`/`UpsertPluginSetting` and the WASM host
   surface (plugins have no settings-write path at all, only
   `settings_get`).
3. **Superfluous named return / missing `pluginObs.trace`** (nitpick) —
   fixed: added the same `trace`/`done(err)` pattern every other
   settings-write method in the file already carries.
4. **No-nesting caveat undocumented vs. the sibling method** (nitpick) —
   fixed: added the same warning `MergeAdditiveJSONMapSetting` carries.
5. **Schema still can't enforce the invariant for other writers** (e.g.
   the LAN-sync apply path) — real future work, out of scope for this
   fix (migrations are append-only, and adding the index needs a dedupe
   step for any till that already has duplicates). Filed as
   universaltill/ut-docs#787.
6. **Existing duplicates aren't retroactively repaired** — informational;
   they self-heal lazily via `ReconcilePluginSettings` on the plugin's
   next install/upgrade. Filed as universaltill/ut-docs#788.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...` — clean.
- `gofmt -l` — clean on both changed files.
- Full `go test ./...` (every package in the module) — clean.
- `go test ./internal/data/... -race` (full package, not just the new
  tests) — clean, including `MergeAdditiveJSONMapSetting`'s existing
  concurrency tests (confirms no regression on the sibling method the fix
  mirrors).
- `go test ./internal/pages/... -run 'TestPluginSettings|TestImportPage' -race`
  — the actual caller's test suite — clean. (The full `internal/pages`
  package under `-race` exceeds the 600s default per-package timeout
  regardless of this change — confirmed pre-existing by running the one
  test that surfaced in a bare `go test ./...` timeout dump, in isolation,
  against unmodified `main`: passes in 24s. Not this diff's regression.)
- `bash scripts/ci/guard-data-access.sh`,
  `bash scripts/ci/guard-kiosk-engine.sh`,
  `bash scripts/ci/guard-plugin-menu-read.sh`,
  `bash scripts/ci/guard-i18n.sh` — all clean.
- No `money.Money`, no user-facing strings, no compliance wording
  (backend-only, no UI/i18n/manual surface touched). Test fixtures use
  `com.example.tax` only, no real client/shop name.

## Safe-to-merge verdict

**Safe to merge.** Fix is correct and independently re-verified; the one
real finding (weak regression test) was fixed and re-verified before this
record was written, not deferred. Two genuine follow-ups filed
(universaltill/ut-docs#787, #788) rather than folded into this diff's
scope.
