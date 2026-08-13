# Code review: plugin binary missing on join → silent wrong tax

**Date:** 2026-08-13
**Card:** ut-docs#368
**Scope:** `internal/plugins/wasm_runtime.go`, `internal/data/plugin_repo.go`,
`internal/data/sync_plugins_repo.go`, `internal/pages/sync_admin.go`,
`internal/pages/tax_hook.go`, `internal/pos/service.go`,
`internal/pages/pos_api.go`, `internal/pages/self_order_shop.go`,
`web/ui/pages/plugins.html`, `web/locales/{en,ar,fa,tr}.json`,
`web/help/{en,ar,fa,tr}/plugins.md`, plus tests; `ut-docs/adr/0011-multi-till-sync.md`
(separate repo, PR universaltill/ut-docs#627).

## What shipped

A joined replica till inherits a plugin's DB row from the primary's join
snapshot but never its binary file. Pre-#368, `WasmRuntime.Sync`'s boot-time
load failure was one log line and nothing else — no state flip, no tally —
and `pluginTaxRateAsker.AskTaxRateBP`'s caller had no way to distinguish "no
tax plugin was ever installed" from "a tax plugin IS registered but
currently broken," so a broken tax plugin's lines silently fell back to the
item's base rate: two tills in one shop could print different tax for the
same item, with no visible signal (p1/security, money-path).

Per the product owner's recorded decision (2026-08-13, ut-docs#368 comment):
**fail closed, scoped narrowly** — block only the sale lines a broken tax
plugin owns, never the whole till; never silently substitute a fallback
rate; surface it loudly.

- `WasmRuntime.Sync` now flips a load-failed plugin to `install_state='broken'`
  (via the existing `PluginRepo.SetPluginState` — no new repo method needed)
  and mirrors it onto the marketplace `InstallStatusStore`; a later successful
  load flips both back (self-heal).
- `pos.TaxRateAsker`/`pluginTaxRateAsker.AskTaxRateBP` widened from `(int,
  bool)` to `(int, bool, bool)` — a third `blocked` return, chosen over a
  sentinel error because a blocked verdict is a deterministic, cacheable
  answer per payload/generation, not a transient failure.
- The cashier tender handler (`pos_api.go`) and kiosk self-order checkout
  (`self_order_shop.go`, via `d.KioskEngine` — never `d.Engine`) both reject
  a tax-blocked tender before payment authorization, with a translated
  operator/customer message.
- `convergePluginSet` (LAN sync) now treats "broken at the correct version"
  as drift to heal, re-fetching through the same Ed25519-verified
  marketplace path a version mismatch already used.
- New "Broken" chip on `/plugins`; 4 new i18n keys × 4 locales; manual topic
  extended; ADR-0011 amended in a matching, separate `ut-docs` PR.

## Build process

Dev built inline via a Sonnet subagent (`complexity:hard` → normally Fable,
but this session runs on Sonnet — see the cycle's own model-routing note),
implementing and testing but not committing. Reviewer ran independently at
Opus, in an isolated git worktree, with instructions to actually run things
and independently re-verify TDD claims — not read the diff and trust it.

## Independent review (Opus subagent, isolated worktree, fresh context)

Verdict on first pass: **do not merge as-is** — one blocker, everything
else solid.

### BLOCKER — generation bump landed before the broken flip, not after

`WasmRuntime.Sync`'s real order: `bus.ResetSubscribers()` bumps the bus
generation once at the top, *then* attempts each plugin's load, *then*
flips `install_state='broken'` on a failure — with no further bump
afterward. `pluginTaxRateAsker` memoizes "is any tax plugin broken" per bus
generation. When the broken plugin is the last (or only) one Sync
processes — guaranteed in this card's canonical scenario, since a broken
plugin never reaches `Subscribe` (the only other thing in that loop that
bumps generation) — a concurrent ask landing between the top-of-Sync bump
and the flip caches "not broken" against the generation the whole pass
settles on. With no further bump, that stale cache entry never
invalidates: every later ask, including ones long after `Sync` returns,
keeps reading it. **This silently reintroduced the exact bug the card
exists to fix**, and the exposure window lasts until the next `Sync`
call — on a standalone till, potentially the whole trading day.

The reviewer reproduced this with a hand-driven probe modeling `Sync`'s
real internal ordering, confirmed a one-line fix (`bus.BumpGeneration()`
inside the existing `if failedCount > 0` block, after the flip loop)
closes it, ran the full regression suite with the fix applied, then
reverted the probe and the candidate fix before finishing (per the
reviewer skill: findings, not fixes, are the subagent's job).

### Real, non-blocking (addressed below) / nitpicks (backlog card filed)

- `ListBrokenPluginsForHook`'s scoping (only a broken, active plugin,
  registered for the exact event asked about) was correct by construction
  but had no pinned test.
- `pos_api.go`'s toast fell back to an empty plugin name on a lookup error
  (cosmetic).
- Three nitpicks (bare `"broken"` literal vs. an unexported constant in a
  different package; hardcoded chip colors matching 3 sibling chips
  already in the file; no deterministic `ORDER BY` on
  `ListInstalledPlugins`) — filed as ut-docs#628 rather than expanding an
  already-substantial diff further.

Full independent gate run (uncached): `go build`, `go vet`, `gofmt -l`
(clean on this diff), `go test` on all 4 touched packages, all 5 CI
guards, plus scoped `-race` — all green. TDD re-verification done for
real (revert → confirm fail with the original bug's exact signature →
restore → confirm pass) on `TestSyncPullTick_ReinstallsBrokenPluginAtMatchingVersion`
and the tax fail-closed tests, including the kiosk path (without the fix,
**a customer completes a tax-blocked purchase through self-order** — 200,
not the required 409).

## Fixes applied after review

1. **The blocker.** Added `bus.BumpGeneration()` in `WasmRuntime.Sync`,
   strictly after the failure-marking loop, inside `if failedCount > 0`.
2. **A real regression test for the ordering itself**, not just the fix's
   surface behavior — added to `internal/plugins/wasm_sync_broken_test.go`
   (drives the actual `Sync` call and asserts the bus generation moves by
   **at least 2** across a pass that marks a plugin broken: one for
   `ResetSubscribers`, one strictly after the flip). Verified this test
   fails with the exact "generation only moved by 1" signature when the
   fix is reverted, and passes when restored — this is what actually pins
   the fix against future regressions in `Sync` itself, since a
   hand-simulated test (asserting the *desired invariant* by manually
   calling `bus.BumpGeneration()`) cannot prove production code calls it.
   A second, narrower test was added to `tax_hook_blocked_test.go`
   reproducing the same race at the tax-hook-cache layer via the real
   `bus.ResetSubscribers()`/`bus.BumpGeneration()` sequence (also
   independently confirmed to fail pre-fix, pass post-fix).
3. **`ListBrokenPluginsForHook` scoping test** — new
   `internal/data/plugin_repo_broken_hooks_test.go`, pinning that only a
   broken+active plugin with an active hook for the exact event asked
   about is ever returned (an unrelated broken plugin, a disabled broken
   plugin, a broken plugin with an inactive hook row, and a healthy plugin
   are all seeded and asserted absent from the result).
4. **The empty-name toast.** Falls back to a new, translated
   `pos.toast.tax_blocked_unnamed_plugin` key (4 locales) instead of
   rendering "Tax plugin  is broken" with a blank gap.

Re-ran the full gate after every fix: `go build ./...`, `go test ./...`
(uncached, whole module — all green), all 5 guards, `gofmt -l` on every
touched file.

## Verified beyond automated tests

- Personally re-read the tax-critical diffs (`tax_hook.go`, `service.go`,
  `pos_api.go`, `wasm_runtime.go`) line-by-line before handing off to
  review, independent of both the Dev subagent's and the reviewer's own
  passes.
- Independently confirmed the `-race` full-suite timeout risk observed
  during this cycle (`internal/plugins` at ~562s on clean `main`, 4-CPU
  sandbox, against a 600s default) is a pre-existing environmental
  characteristic of this cloud session's hardware, not a defect in this
  diff — reproduced on unmodified `origin/main` before any code review
  began, and confirmed the project's actual CI (`.github/workflows/ci.yml`)
  runs plain `go test ./...`, never `-race`.
- Confirmed the ADR-0011 amendment (separate `ut-docs` repo,
  universaltill/ut-docs#627) matches the shipped behavior exactly,
  including the DB-error fail-open exception now tracked in ut-docs#628.

## Safe to merge

Yes, after the blocker fix and the three items above. All gates green,
full independent review completed, TDD claims re-verified twice (once by
the reviewer, once by re-running the reviewer's own regression tests
myself after applying the fix).

## Deferred / follow-up

- ut-docs#628 — install-state constant consistency, chip color
  tokenization (matches 3 pre-existing sibling chips), deterministic
  `ORDER BY` on `ListInstalledPlugins`, ADR mention of the DB-error
  fail-open exception.
- Immediate post-join `kick`-triggered sync tick (currently waits up to
  30s) — correctness doesn't depend on it post-fix (fail-closed holds
  throughout the window), so left as a latency-only follow-up per Dev's
  own scoping note, not filed as a separate card since it was already
  out of scope going in.
