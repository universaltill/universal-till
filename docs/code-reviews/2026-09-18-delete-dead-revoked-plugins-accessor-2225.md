# Code review — delete dead revocation read-back accessor pair (ut-docs#2225)

- **Date:** 2026-09-18
- **Ticket:** ut-docs#2225 (`p3`, `complexity:easy`, `infrastructure`)
- **Branch:** `fix/2225-delete-dead-revoked-plugins-accessor`
- **Reviewer:** independent pass, fresh-context Sonnet subagent (per this
  card's `complexity:easy` routing, `MODEL-ROUTING.md`).
- **Verdict: SAFE TO MERGE.** No findings, no nits.

## The card

ut-docs#2225 (itself a correction of an earlier, wrongly-premised finding
from the ut-docs#1566 `internal/data` deadcode slice) established that
revocation **enforcement is live in production** — `internal/server` runs
`RevocationChecker.SyncRevocations` on a 30-minute ticker, and
`processRevocation` disables a revoked plugin directly via
`PluginRepo.GetPlugin`/`SetPluginState`, never through any read-back path.
What's actually dead is a separate **read-back accessor pair**:

```
PluginRepo.ListRevokedPlugins        (internal/data — no production caller)
  ← RevocationChecker.GetRevokedPlugins  (internal/plugins — unreachable)
```

`GetRevokedPlugins` exists to list currently-revoked plugins (e.g. for a
future admin display showing which plugins were disabled and why), but no
such display was ever built and nothing calls it. The card asked for a
decision: wire it into a real admin-facing feature, or delete the pair.

## The decision (BA/Architect call this cycle)

**Delete.** No admin UI/design exists to wire this into, there's no open
card asking for a "revoked plugins" display, and the repo's own standards
(`CLAUDE.md`: no speculative abstractions, no half-finished features) argue
against building a UI nobody has asked for yet on the strength of an unused
accessor. If a real "revoked plugins" display is wanted later, it's cheap
to re-add against `install_state = 'revoked'` directly. No ADR needed —
pure dead-code removal, no behavioral or architectural change.

## What shipped

- `internal/plugins/revocation.go`: deleted `RevocationChecker.
  GetRevokedPlugins` (33 lines including its doc comment). The `data`
  package import stays — `SyncRevocations`/`processRevocation` still use
  `data.NewPluginRepo(rc.db)` directly.
- `internal/data/plugin_repo.go`: deleted `PluginRepo.ListRevokedPlugins`
  (37 lines including its doc comment) and a stray duplicate blank line
  left behind. `PluginInfoRow` (its return type) is untouched and still
  used by `PluginRepo.GetPlugin`.
- `internal/plugins/revocation_test.go`: removed the trailing
  "`GetRevokedPlugins` surfaces it" assertion block from
  `TestSyncRevocationsDisablesInstalledPlugin` — that test's direct SQL
  assertions of `is_active`/`install_state` right after `SyncRevocations`,
  its audit-log check, and its second-sync no-op check are all untouched
  and still cover the real enforcement path end to end.
- `internal/data/plugin_repo_lifecycle_test.go`: deleted
  `TestSetPluginStateAndListRevokedPlugins` in full (it existed
  specifically to exercise `ListRevokedPlugins`). `SetPluginState` is not
  left untested: `TestUpdatePluginVersionAndSetPluginActive` in the same
  file still exercises it directly, and `TestSyncRevocationsDisablesInstalledPlugin`
  exercises it through the real `processRevocation` call path.

Net: 4 files changed, 108 deletions, 0 additions.

## What the independent review found

**Verified independently, not trusted from the diff alone:**
- Zero remaining references to `GetRevokedPlugins` or `ListRevokedPlugins`
  anywhere in the repo (including comments/docs), and none in `ut-cloud`
  either — no cross-repo contract on either symbol.
- No reflection/string-based lookup of either name anywhere (ruling out a
  dynamic-dispatch caller a plain grep for the identifier would miss).
- `internal/plugins/marketplace/**` (the separate, unrelated ut-docs#2386
  dead-client finding) is untouched by this diff — confirmed via
  `git diff --name-only`.
- Both symbols were unexported-package internals; no compatibility
  concern for `ut-cloud` or any plugin loader.

**Full gate run live, all green:**
`gofmt -l .` (clean), `go build ./...`, `go vet ./...`,
`go test ./internal/data/... ./internal/plugins/...` (all `ok`),
`go test ./...` (full repo, 60 packages, no `FAIL`),
`golangci-lint run ./...` (0 issues), `bash scripts/ci/guard-data-access.sh`
(passes — relevant since this touches `internal/data`).

**Findings:** none. No blocker, no nit.

## Explicitly deferred

- ut-docs#2386 (`marketplace.Client.GetRevocations`, a different,
  unrelated dead duplicate client with its own wire-format bug) — out of
  this card's scope, tracked separately.
- Re-adding a "revoked plugins" admin display, if a real design/demand for
  one shows up later — straightforward against `install_state = 'revoked'`
  directly, not resurrecting this exact accessor shape.
