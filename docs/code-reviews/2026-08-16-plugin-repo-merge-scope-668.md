# Code review — MergeAdditiveJSONMapSetting read/write scope agreement (ut-docs#668)

**Date:** 2026-08-16
**Card:** universaltill/ut-docs#668
**Branch:** `fix/plugin-repo-merge-scope-668`
**Reviewer:** independent reviewer, Sonnet, fresh context (per the
`complexity:easy` routing — build and review both at Sonnet, but review
runs as a separate fresh-context instance that never saw the dev
reasoning)

## What shipped

`internal/data/plugin_repo.go` — `PluginRepo.MergeAdditiveJSONMapSetting`'s
`SELECT` now scopes to `AND scope = 'global'`, matching the `UPDATE`/
`INSERT` it already targeted. Before this fix the `SELECT` preferred the
most-specific scope present (register beats user beats global, mirroring
`GetPluginSetting`'s own preference order) while the write always targeted
`scope='global'` — so a register-scoped row for the same key would be read
and merged into the computation, but the merged result written into a
*separate* global row, silently diverging from what `GetPluginSetting`
reports back and leaking till-specific data into the shop-wide row. This
was flagged (but deliberately not fixed) during ut-docs#532's review, since
the only caller (`import_page.go`'s `mergeTakeawayOverrides`, against
`ut-plugin-tax-de`'s `takeaway_rate_overrides` key) is global-scoped only
today, so it was inert. No signature or caller change — pure internal fix.

Doc comment rewritten to state the global-only read/write scope plainly,
replacing the old "tracked as a follow-up" note.

New regression test:
`TestMergeAdditiveJSONMapSetting_IgnoresRegisterScopedRow` — seeds a
register-scoped row and a global-scoped row for the same key, calls the
merge twice, and asserts the merge only ever reads/writes the global row
and leaves the register-scoped row byte-for-byte untouched.

## Independent review

Spawned a fresh-context Sonnet subagent (per the `complexity:easy` model
routing) with the exact diff, the issue's requirement, and instructions to
run the build/tests personally and independently re-verify the TDD claim
by reverting the fix, re-running the new test, and confirming it fails for
the right reason before restoring the fix.

**Verdict: safe to merge, no blockers, no should-fix items.**

Independently confirmed:
- `go build ./...`, `go vet ./...` clean.
- `go test ./internal/data/... -run TestMergeAdditiveJSONMapSetting -v` —
  all 4 tests pass (the 3 pre-existing plus the new one).
- `go test ./internal/data/...` (full package) and `go test
  ./internal/pages/...` (the sole caller's package) — no regressions.
- `bash scripts/ci/guard-data-access.sh` — SQL still confined to
  `internal/data`.
- **TDD re-verification**: reverted just the SQL change (kept the new
  test), re-ran `TestMergeAdditiveJSONMapSetting_IgnoresRegisterScopedRow`
  — failed with `added = 1, want 2 (both "reg_only" and "b" are new to
  the global row the merge reads)`, exactly the described divergence.
  Restored the fix, re-ran — passed. Confirmed a real bug, a real
  regression test, and a real fix, not a claim taken on trust.
- Grepped every call site of `MergeAdditiveJSONMapSetting` and every call
  site of `UpsertPluginSettingScoped` — confirmed no caller anywhere in
  the codebase relies on the old scope-fallback behavior (nothing writes
  a register/user-scoped row for `takeaway_rate_overrides` today).
- No secrets, no real client/shop name in the diff.
- SQL injection: none — both queries are fully parameterized; the only
  literal is the fixed string `'global'`.

One **nice-to-have, out of scope, not applied** — filed as a follow-up
card instead of folded into this diff: `UpsertPluginSettingScoped` does
its update-then-insert as two separate non-transactional statements, and
the table's `UNIQUE (plugin_id, key, scope, scope_id)` doesn't actually
block duplicate `scope='global'` rows because `scope_id` is `NULL` there
and SQLite treats `NULL`s as distinct in unique indexes (already flagged
in that method's own comment). If two such duplicate global rows ever
existed, this fix's now-scope-exact `SELECT ... LIMIT 1` would arbitrarily
pick one to read, while the `UPDATE ... WHERE scope='global'` (no row-id
filter) would silently update *both* — masking rather than surfacing the
integrity problem. Pre-existing, not introduced or worsened by this diff;
tracked as universaltill/ut-docs#785.

## Scope discipline

- Diff touches exactly two files: `internal/data/plugin_repo.go` (SQL +
  comment) and `internal/data/plugin_repo_merge_setting_test.go` (new
  test). No UI, no template, no locale string, no migration — `guard-i18n.sh`,
  `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`,
  `guard-help-topics.sh` don't apply to this diff.
- No caller changes; the only real caller (`import_page.go`) is untouched.

## Gate

| Check | Result |
|---|---|
| `go build ./...` | pass |
| `go vet ./...` | pass |
| `go test ./...` (full suite) | pass, all packages |
| `scripts/ci/guard-data-access.sh` | pass |
| `scripts/ci/guard-kiosk-engine.sh` | pass |
| `scripts/ci/guard-plugin-menu-read.sh` | pass |
| `scripts/ci/guard-i18n.sh` | pass |

## Files

- `internal/data/plugin_repo.go` — scope the `SELECT` inside
  `MergeAdditiveJSONMapSetting` to `scope='global'`, matching the write;
  rewrote the doc comment accordingly.
- `internal/data/plugin_repo_merge_setting_test.go` — new regression test
  proving read/write scope agreement.
