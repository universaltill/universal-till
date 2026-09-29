# Review: one lock for Engine, KioskEngine and self-order session config applies (ut-docs#3085)

**Date:** 2026-09-29 · **Lane:** lane:cloud-24 · **Built by:** Claude Sonnet (subagent) · **Reviewed by:** Claude Opus 5.5 (independent subagent, fresh context, isolated worktree)

## What shipped
- `internal/pages/engine_config.go`: `applyEngineConfig(d, cfg)` applies a `pos.Config` to the cashier `Engine`, the separate `KioskEngine` (ut-docs#449) and every live table-QR session (ADR-0103) in that order under one package-level mutex. It is the cross-engine twin of `SessionBasketManager.SetConfig`'s `setConfigMu` (ut-docs#2488). Package-level so `common.Deps` stays copyable.
- All four call sites use it: `newRederiveSettings` (init.go, still behind its `Engine.Config() != newCfg` guard), the setup wizard finish (setup_page.go), the store-settings save and the `/api/settings/upsert` tax/country case (settings_page.go). The card named three; setup_page.go was the fourth.
- Nil-safety improved: init.go used to call `dp.KioskEngine.SetConfig` unchecked.
- `web/help/img/manifest.json`: docs-shots surface hash refreshed; no rendered pixel changed (Go-only, no template/string edits).

## Findings
1. **Minor, pre-existing, out of scope → Backlog ut-docs#3245:** each caller computes the config from state read before the lock, so an older save can still apply last (engines agree with each other, but on the older value until the next save). The init.go compare also runs outside the lock. #3085's invariant (the three never diverge) holds.
2. **Nit, accepted:** the test's 200 ms window could miss a regression on a pathologically slow box (it cannot fail falsely with the lock in place). Same shape as the #2488 precedent.
3. **Nit, accepted:** init.go still dereferences `dp.Engine.Config()` unchecked; Engine is always set.

Reviewer checked and found nothing: deadlock (lock order `engineConfigMu` → `Service.mu`; `recomputeTotals` drops `s.mu` around the plugin ask; no plugin host function or ask path re-derives settings; loopback to the till's own port is blocked for WASM egress), missed call sites (only `engine_config.go` and the per-session fan-out in `pos/session_manager.go` call `SetConfig`), sessions minted mid-apply (created under `m.mu` from the kiosk engine's current config). No SQL, money or i18n surface.

## Verified
- TDD: `TestEngineConfig_ConcurrentAppliesDoNotInterleave` (kiosk engine held mid-apply by a blocking charge-policy asker) failed without the lock — `Engine already has cfg2 while call 1 is still mid-apply: batches interleaved` — re-verified independently by the reviewer in its own worktree; passes with it (`-race -count=5`).
- `gofmt`, `go build ./...`, `go vet`, full `go test ./...`, `golangci-lint` (pages), every `ci.yml` build-job guard. Locally-unrunnable only: `guard-shellcheck-version` (no shellcheck binary) and `guard-deadcode-baseline` (fails identically on `main` here — no GTK headers, so the desktop root is skipped; CI analyzes all roots).
- No visual surface changed; nothing looked at.

**Verdict:** safe to merge.
