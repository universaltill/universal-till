# Code review: seal ReconcilePluginSettings' default-value seeding (ut-docs#1746)

## What shipped

Closes the gap the ADR-0082/ut-docs#1739 implementation review deferred:
`ReconcilePluginSettings` (install/upgrade-time default-value seeding in
`internal/data/plugin_repo.go`) wrote manifest `default_value`s straight to
`plugin_settings.value_json`, bypassing the seal/open seam entirely — a
manifest `default_value` on a secret-typed (`type: "secret"`) or
heuristic-matching (`secrets.IsSecretSettingKey`) key landed in cleartext
at install time, falsifying `sealSettingValue`'s own doc comment that no
caller can bypass it.

- `PluginSettingRow` gains `DeclaredSecret bool`, set by
  `internal/plugins/manifest.go` from `m.SettingDeclaredSecret(s.Key)` — the
  caller has the manifest, mirroring `UpsertPluginSettingScoped`'s existing
  `declaredSecret` parameter.
- `ReconcilePluginSettings`'s new-row insert path now runs the value
  through the same `sealSettingValue` seam every other plugin-settings
  writer in the file uses, failing the whole reconcile closed (no row
  written) when sealing fails — same policy `UpsertPluginSettingScoped`
  already applies.
- New `internal/plugins/main_test.go` registers a throwaway self-generating
  `secrets.KeyStore` for that package's test binary, mirroring
  `internal/data`'s and `internal/pages`' own `TestMain` — several existing
  `PersistManifest` fixtures (`setting_key_validation_test.go`) use
  realistic key names like `"api_key"` with a non-empty default value,
  which now legitimately need one to install.
- Two new regression tests in `internal/data/plugin_repo_secret_settings_test.go`.

## Independent review (fresh-context Sonnet subagent, per this card's `complexity:easy` routing)

Full review ran in an isolated worktree, with real command execution
(`go build`, `go vet`, `gofmt -l`, `go test` on `internal/data`,
`internal/plugins`, `internal/pages`, `golangci-lint`,
`guard-data-access.sh`) and its own independently-reverted TDD
verification (reverted the `sealSettingValue` call, confirmed both new
tests fail with the exact expected symptom, restored, confirmed green
again).

**Verdict: SAFE TO MERGE.**

- **Should-fix (deferred — follow-up filed, ut-docs#1752):** the fix seals
  new rows going forward but does not retroactively reseal a row that was
  already written in cleartext by the bug it closes.
  `ReconcilePluginSettings`'s existing-row branch only ever updates `scope`
  for a row that's already present, never `value_json` — confirmed
  empirically with a scratch test the reviewer wrote and deleted (seeded a
  pre-#1746-style cleartext default row directly, ran the fixed
  `ReconcilePluginSettings` again with the same manifest, and the row
  stayed unsealed). Not a blocker: the read side already tolerates legacy
  plaintext by design, and the product is pre-first-paying-shop (ADR-0074).
  Filed as ut-docs#1752.
- **PASS, verified independently:**
  - `sealSettingValue`'s signature matches the new call site exactly.
  - `DeclaredSecret` threading: the only production caller of
    `ReconcilePluginSettings` (`internal/plugins/manifest.go`'s
    `PersistManifest`) sets it correctly from the manifest; no other call
    site constructs a `PluginSettingRow` for a secret-typed setting while
    leaving it unset.
  - Production fail-closed risk: `internal/app/app.go` wires
    `secrets.SetDefault(...)` before `plugins.Init(...)`, so "install fails
    hard because no key store is registered yet" is not a real production
    scenario — the new `internal/plugins/main_test.go` is legitimately
    fixing test fixtures to match production reality, not masking a gap.
  - The pre-existing "best candidate" duplicate-row comparison logic
    (unmodified by this diff) is only exercised when duplicate rows already
    exist for one key; with a single existing row it only ever touches
    `scope`, confirming the should-fix above is real and precisely scoped.
  - `TestReconcilePluginSettingsUpgrade` (pre-existing) still passes and
    still means what it claims.
- **Recurring bug classes checked, neither applies:** no new file-write
  handler (missing `os.MkdirAll`); no path construction at all in this
  diff (`paths.Data(...)` n/a).
- **No scope creep:** the diff does exactly what the ticket asked, nothing
  more, for new installs.
- No real client/shop name or literal credential anywhere in the diff — all
  test fixtures use obviously-fake values (`sk_default_from_manifest`,
  `M-DEFAULT`, `com.example.pay`).

## What was verified beyond automated tests

- TDD claim re-verified personally before requesting review: reverted the
  `sealSettingValue` call in `ReconcilePluginSettings`'s insert path back to
  writing `d.ValueJSON` directly, re-ran both new tests, confirmed they
  fail with the exact claimed symptom (plaintext at rest / no fail-closed
  error), restored the fix, confirmed both pass again.
- Independently re-verified a second time by the review subagent in its own
  isolated worktree, same revert/restore method, same result.
- Full gate run clean: `gofmt -l .` empty, `go build ./...` clean,
  `go vet ./...` clean, `go test ./...` fully green across the whole repo
  (no `internal/pages`/`internal/plugins` regressions from the new
  `TestMain` or the fail-closed default-value seeding), `golangci-lint run
  ./...` 0 issues, every CI-blocking guard in the build job passes
  (`guard-data-access.sh` and the rest — this is a pure Go backend change,
  no UI/i18n/kiosk/compliance surface touched).

## Safe-to-merge verdict

**Safe to merge.** The bypass the ticket describes is closed for the case
it names (install-time default-value seeding). The one gap the review
found (pre-existing unsealed rows on upgrade) is real but bounded, already
tolerated on the read side, and tracked as an independent, precisely-scoped
follow-up (ut-docs#1752) rather than silently dropped.

## Explicitly deferred (follow-up card)

- ut-docs#1752: reseal a pre-existing unsealed secret-setting default row
  the next time `ReconcilePluginSettings` runs for that plugin, not just
  seal it at first insert.
