# Boot-time builtin-layout reconciliation (ut-docs#2001)

## What shipped

`internal/plugins/builtinlayouts.Sync` was previously only ever called from
the two HTTP write handlers that set `common.KeyShopType`
(`internal/pages/setup_page.go`, `internal/pages/settings_page.go`'s
`/api/settings/shop-type`). A shop that already had `shop_type=service`
persisted — from before this wiring existed (ut-docs#1902), after a DB
restore, or after a manual plugin uninstall — got nothing until an operator
happened to re-save the same dropdown value.

`internal/pages/init.go`'s `Init()` now reconciles once on every boot, right
after `dp := &common.Deps{...}` is constructed: reads the persisted
`shop_type`, calls `builtinlayouts.Sync(ctx, db, shopType)`, and on success
calls `dp.ReloadPlugins(ctx)` so the in-memory menu/amendments pick it up
immediately rather than waiting for the next plugin lifecycle event. Errors
are logged as warnings and never block boot — same best-effort stance as the
two existing handler call sites (a boot must never be blocked over a
cosmetic menu personalization).

## Tests (TDD, failing-then-passing confirmed)

- `TestInit_ReconcilesBuiltinLayoutForPreExistingShopType` — seeds
  `shop_type=service` directly via the settings store (bypassing both write
  handlers, simulating the exact gap this card describes), boots `Init`,
  and asserts the salon plugin is installed and its `/tables`-hiding
  amendment is live. Confirmed failing pre-fix (`found=false`, empty
  amendments) before the `init.go` change, passing after.
- `TestInit_LeavesNonServiceShopTypeAlone` — boots with no `shop_type` ever
  set; asserts the salon plugin is NOT installed (negative control against
  over-installation).

## Review

Independent review: Opus, fresh context, isolated git worktree (this card
is `complexity:medium` — Sonnet built it, Opus reviewed it per this
pipeline's model-routing rule). **Verdict: PASS, no blockers.**

Checks the reviewer ran directly (not just read the diff): `go build`,
`go vet`, full `go test` on `internal/pages` and
`internal/plugins/builtinlayouts`, `gofmt -l`, `golangci-lint run`,
`guard-data-access.sh`, `guard-i18n.sh`, `guard-plugin-menu-read.sh` — all
clean. The TDD claim was independently re-verified via two separate
mutations (removing the whole reconciliation block, and removing only the
`ReloadPlugins` arm) — both reproduced the documented pre-fix failure
(plugin not installed / amendments empty), confirming neither half of the
fix is a no-op and the test isn't tautological (it genuinely drives the
boot path, not a re-test of `Sync` in isolation, since `pm.LayoutAmendments`
here can only be populated by `Init`'s own `ReloadPlugins` call).

The reviewer also specifically chased and ruled out: a replica
install/uninstall flap (the salon plugin isn't listing-backed, so the sync
loop can't prune it), a plugin-signature-verification bypass (the salon
manifest declares `"runtime": "none"` — no code ever executes, so the
verifier isn't in the picture), the two recurring bug classes this
pipeline watches for (missing `os.MkdirAll`, cwd-relative paths — neither
present; `installSalon` already does both correctly), the empty-`shop_type`
case (maps to a safe no-op), and a double-`Sync` boot race (idempotent by
design).

### Findings — both low severity, deferred, not fixed in this branch

1. **LOW — unconditional `ReloadPlugins` duplicates `plugins.Init`'s own
   work on every boot when `Sync` is a no-op** (the overwhelmingly common
   case — any non-service shop, or a service shop already at the current
   version). Concrete effect: any operator-facing startup warning
   `Manager.Reload` can emit (e.g. an orphaned plugin-owned payment method)
   now prints twice per boot, reading like two distinct problems in the
   log. No WASM recompile, so not a latency concern — log noise plus one
   redundant `SyncPluginPaymentMethods` write. Properly closing this means
   giving `Sync` a `changed bool` return so the caller can skip the reload
   on a genuine no-op — a real but separable follow-up, not a change to
   this card's own scope. **Filed as ut-docs#2006.**
2. **LOW, pre-existing pattern (not introduced by this diff)** — on
   `builtinlayouts.Sync`'s remove-then-reinstall path (a stale embedded
   version bumped across a self-update), if `removeSalon` succeeds but the
   subsequent `installSalon` fails, `Sync` returns an error and the
   `else if` chain at all three call sites (the two pre-existing handlers,
   and this new one) skips `ReloadPlugins` — leaving the in-memory
   `LayoutAmendments` still carrying amendments for a plugin the DB now
   says is uninstalled, until the next reload/restart. This directly
   contradicts `removeSalon`'s own doc comment (file-removal failure is
   swallowed specifically so "the caller's `ReloadPlugins` always runs").
   Narrow trigger (a version bump *plus* a failing reinstall), and the
   residual symptom is roughly aligned with intent (`shop_type` is still
   `service`, so still hiding `/tables`), hence low. This diff follows the
   established convention rather than inventing a worse one — the tidier
   fix (log `Sync`'s error, then reload unconditionally) belongs to
   `builtinlayouts` itself, across all three call sites at once, not to
   this card alone. **Filed as ut-docs#2006** (same follow-up card as
   finding 1 — both are `builtinlayouts`/`ReloadPlugins`-contract
   refinements).

## Verified beyond automated tests

No UI surface touched (pure backend startup-wiring change — no template,
no new locale key, no new HTTP-visible behavior beyond the reconciliation
itself), so no screenshot/visual check applies. `go test ./...` (full
suite, all packages) green before opening the PR.

## Safe to merge

Yes — PASS verdict, both findings deferred to a Backlog follow-up
(ut-docs#2006), full gate green.
