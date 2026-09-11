# Code review: push plugin-settings BumpGeneration structurally instead of enforcing it by CI grep (ut-docs#1941)

**Card:** universaltill/ut-docs#1941 — follow-up from the independent
review of ut-docs#1357 (`docs/code-reviews/2026-09-09-plugin-settings-bump-guard-1357.md`).
**Complexity:** hard (Dev: Fable subagent; Review: Opus subagent, fresh
context, independent from the Dev pass, isolated worktree).

## What the card asked for vs. what shipped

`internal/data/plugin_repo.go`'s three plugin-settings writers
(`UpsertPluginSetting`, `UpsertPluginSettingScoped`,
`MergeAdditiveJSONMapSetting`) relied on every caller separately
remembering to call `plugins.SharedBus(db).BumpGeneration()` afterward, so
an `.ask`-hook asker's memoized answer gets invalidated. This convention
was enforced only by a CI grep guard, and had already been broken twice
for real (ut-docs#222, ut-docs#1351 — a live VAT over-collection bug in
the Germany café pilot).

Architect scoped the two real production call sites precisely before
Dev started: `internal/pages/plugin_settings_page.go`'s
`registerPluginSettings` (both its own generic-settings loop and the
`writeTaxOverrides` helper it calls, sharing one `repo`), and
`internal/pages/import_page.go`'s `mergeTakeawayOverrides`.
`UpsertPluginSetting` (unscoped) had zero production callers.
`internal/data/fiscal_repo.go`'s own `NewPluginRepo(db)` construction
never calls any of the 3 writers (uses only plugin *storage*, not
*settings*), so it needed no change.

## What shipped

- `internal/data/plugin_repo.go`: `PluginRepo` gets a private
  `onSettingsChanged func()` field and a chainable
  `OnSettingsChanged(fn func()) *PluginRepo` setter. No import of
  `internal/plugins` — the field is a bare `func()`, so the cycle
  `internal/plugins → internal/data` that motivated the original guard
  stays broken exactly as it was; nothing here reverses it.
- `UpsertPluginSettingScoped` (which `UpsertPluginSetting` delegates to)
  now reads the row's prior stored value inside the same write-locked
  transaction, computes whether the write actually changes anything, and
  fires the hook after `tx.Commit()` succeeds, only when something
  changed. The compare happens on the **post-seal** value (sealing runs
  before the transaction opens), so a sealed secret's fresh-nonce-every-
  write behavior always counts as changed.
- `MergeAdditiveJSONMapSetting` fires the hook after its own commit,
  reachable only when `added > 0` (its `added == 0` early return skips
  the write entirely) — mirroring the gate `mergeTakeawayOverrides` used
  to apply caller-side.
- The two real call sites construct with
  `.OnSettingsChanged(func() { plugins.SharedBus(db).BumpGeneration() })`
  and their now-redundant manual `if changed/added > 0 { BumpGeneration() }`
  blocks are deleted; the `changed`/`added` counters, audit logging, and
  every error path are otherwise untouched.
- New test: `internal/data/plugin_repo_settings_changed_hook_test.go` —
  fires on insert, fires on changed value, does not fire on an identical
  re-write, fires on a declared-secret write (fresh seal nonce) every
  time, fires only when `MergeAdditiveJSONMapSetting` actually adds
  something, and a nil hook never panics on any of the three writers.

## Independent review (Opus, fresh context, isolated worktree)

Ran `gofmt`/`go vet`/`go build`/`golangci-lint` and the full
`internal/data`/`internal/pages`/`internal/plugins` suites personally,
plus `go test -race` on the new tests. Re-verified the TDD claim by
reverting `internal/data/plugin_repo.go` alone, confirming the new test
fails to build (red), then restoring it and confirming green. Read every
changed/new file in full, not the diff hunks alone, and specifically
checked: the new SELECT's WHERE clause matches the UPDATE/INSERT's target
row exactly (`scope_id` exists in the schema but is never non-NULL for any
row these methods manage, so no ambiguity); the compare is genuinely
post-seal; the hook only fires after a successful commit on every path;
`UpsertPluginSetting` inherits the hook via delegation; nothing besides
the redundant bump call was caught in either deleted block; and (via
direct grep + reading) there is no other production caller of the three
writer methods anywhere in the repo that the diff missed.

**One important, design-level finding — fixed, not deferred (N1):** the
diff as first drafted *deleted* `guard-plugin-settings-bump.sh` outright.
The bump moved into the writer, but *attaching* the hook is still a
per-construction-site opt-in — there is no shared, pre-hooked
`PluginRepo` (`common.Deps` holds a bare `*sql.DB`, not a repo) — so
deleting the guard traded a CI-enforced invariant for an unenforced
convention, for the exact bug class that has already shipped to
production twice. Concrete failure scenario the reviewer constructed: a
future caller (e.g. a plugin-rollback "restore previous settings" step)
calling `data.NewPluginRepo(db).UpsertPluginSetting(...)` with no hook
attached would compile clean, pass every existing test, and CI would go
green while silently reproducing ut-docs#1351's exact shape. **Fix
applied**: restored `guard-plugin-settings-bump.sh` and its regression
test, retargeted from checking for a same-file `BumpGeneration()`
reference to checking for a same-file `OnSettingsChanged(` reference —
the guard's structure, per-line allow-comment escape hatch, and
fail-closed behavior on a matched-nothing rename are all unchanged, only
the token it looks for moved to match the new convention. Restored the
two corresponding `.github/workflows/ci.yml` steps. Verified: the guard
passes on the real (fixed) codebase, and its own regression suite (10
cases: missing-hook rejection in `internal/`, `cmd/`, `scripts/`, `e2e/`;
correctly-wired pass; test-file exemption; both same-line-allow cases;
fail-closed-on-rename; clean-codebase baseline) all pass.

**Two smaller findings, also fixed:**
- `internal/pages/tax_hook.go`'s doc comment on `pluginTaxRateAsker`
  still described the old mechanism ("the settings endpoint … call[s]
  BumpGeneration"). Updated to say the writers bump structurally via
  `PluginRepo.OnSettingsChanged` now.
- `OnSettingsChanged`'s doc comment now states explicitly that it must be
  set once, at construction, before the repo is shared with a concurrent
  caller (current usage is already safe — `go test -race` clean, always
  set in the same expression as the constructor — this is documentation
  against a future misuse, not a bug fix).

**Checked and dismissed, not just skipped** (reviewer's own list,
confirmed by direct reading rather than re-derived here): the `LIMIT 1`
SELECT against a possible duplicate row (closed by the same
`_txlock=immediate` serialization that motivated ut-docs#785, and
`scope_id` is always NULL for these rows); `sql.NullString` against a
`NOT NULL` column; bump frequency going from once-per-request to
once-per-changed-setting (pure, cheap over-invalidation, no correctness
risk); no deadlock/re-entrancy risk (hook runs after the write lock is
released, only takes the event bus's own mutex); no startup-ordering
change (the event-bus singleton is still built lazily inside the closure).
One incidental, unadvertised improvement noted: pre-diff, a save request
that errored on its third setting never reached the bump for the first
two already-committed writes; post-diff each successful write invalidates
immediately, closing that partial-failure hole.

## Verified beyond automated tests

- `gofmt -l .` clean; `go vet ./...` clean; `golangci-lint run` on the
  three touched trees: `0 issues.`
- `go build ./...` green — the real import-cycle check.
- `go test ./internal/data/... ./internal/pages/... ./internal/plugins/...`
  — full green, twice (Dev pass, and again after applying the review
  fixes).
- `go test -race ./internal/data/ -run TestPluginRepo_OnSettingsChanged`
  green.
- TDD red→green personally re-verified (Dev, then independently by
  Review): reverting `plugin_repo.go` alone fails the new test to build;
  restoring it passes.
- `bash scripts/ci/guard-plugin-settings-bump.sh` and its own regression
  test, both green against the retargeted convention.
- `.github/workflows/ci.yml` re-parses as valid YAML after restoring the
  two guard steps.
- No UI surface touched (no `web/`, no template, no new user-facing
  string, no i18n/help/docs-shots obligation) — confirmed by diff stat,
  not assumed.
- No real client/shop name, no literal secret in test fixtures.

## Safe to merge

Yes. No functional defect; the one design-level finding (N1) was fixed
in this same session rather than deferred, since it directly reopened the
bug class this card exists to close.
