# Review: the replica's plugin-update chip is recomputed after following (ut-docs#2984)

## What shipped

Owner report: the tablet (main) updated the fiskaly plugin (tax-de 0.8.0 → 0.9.0), and the Windows additional till followed 3 s later, but it kept showing "Updates are installed from the main till (1)". The chip count is the plugin update scheduler's marketplace-vs-installed check, recomputed every 15 min. `convergePluginSet` now recomputes it (`pluginUpdateTickFn`, count-only on a replica, ut-docs#460) whenever an install or uninstall landed. There's no recompute when nothing changed (the steady state runs every 30 s) or when the install failed. The install call got a test seam (`pluginSyncInstall`).

## Review (Dev: Opus subagent, TDD; independent review: Fable)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix (blocking as written) | The first version decremented the count (`NotePendingUpdateApplied`) on every update-install. But the count means "marketplace newer than installed", so a broken plugin re-fetched at the SAME version (every 30 s in the #368 re-break case), or a main till pinned behind latest, would hide real pending updates. | **Fixed as the reviewer recommended:** recompute instead of guessing. Tests: followed update → 1 recompute (fails with it removed: "recomputed 0 times … want 1"); fresh install → 1; failed install → 0; already following → 0 installs, 0 recomputes. |
| 2 | — | Double-decrement race. | Moot: no decrement any more. |

## Gate

gofmt; `go build`, `go vet`; full `go test ./...`; guard-data-access; guard-docs-shots (fresh).

**Verdict:** safe to merge.
