# Code review — tax.rate.ask answers memoized: per-line wasm boot made scan/tender scale with basket size

- **Date:** 2026-08-01
- **Task:** ut-docs#222 (p1 field report, real Pi4 till: "adding item /
  finishing the payment is too slow" — ~1 s per scan, seconds at tender)
- **Branch:** `fix/tax-ask-per-line-wasm-cost`
- **Author:** interactive pipeline session (Fable)
- **Independent reviewer:** general-purpose subagent on **Opus** (different
  model, per standing practice)

## The bug

With any `tax.rate.ask` subscriber installed (`ut-plugin-tax-uk` landed on
the Pi4 2026-07-31 evening), `recomputeTotals` asks the hook once per basket
line (`internal/pos/service.go`), and every ask instantiates the plugin's
wasm module fresh (`WasmRuntime.HandleEvent`, per ADR-0001's
fresh-instance-per-event design). A plain-Go (`GOOS=wasip1`) module boots
the entire Go runtime per instantiation — **measured ~90 ms on the Pi4**
(the spec had claimed "~ms"; corrected in ut-docs
`architecture/wasm-runtime.md`). Latency therefore grew linearly with
basket size. Measured on the real Pi4 (v0.2.55, isolated second instance,
same catalog):

| basket lines | POST /api/pos/scan | POST /api/pos/tender |
|---|---|---|
| 1 | ~0.10 s | ~0.20 s |
| 8 | 0.71 s | 1.50 s |
| plugin removed | 0.004 s | 0.015 s |

Not a v0.2.53–55 regression — v0.2.52 measured identically once the plugin
was loaded. Each ask also ran a `CheckPermission` query and wrote an
`event_dispatch` audit row, all per line per recompute.

## The fix

- `pluginTaxRateAsker` (internal/pages/tax_hook.go) memoizes the plugin's
  answer keyed by the full ask payload (item, tax code, base rate, order
  type). Clean declines (empty response, or parsed `rate_bp <= 0`) are
  cached too — they cost the same boot. Handler errors and answered-but-
  unparseable JSON are never cached (decline once, retry next recompute).
  Cache is bounded (4096; full drop on overflow — one recompute of re-asks,
  not a wedge). Ask runs outside the lock; a store is dropped if the
  generation moved mid-ask.
- `EventBus.Generation()` — bumped under lock on every subscriber-set
  change (`ResetSubscribers`/`subscribe`/`Unsubscribe`) — invalidates the
  cache across plugin install/update/enable/disable (`Manager.Reload`).
- `EventBus.BumpGeneration()` — explicit invalidation for answer-relevant
  changes that don't resubscribe (see review findings below): plugin
  settings save, sync/directive-applied settings (`rederiveSettings`),
  permission grant/revoke.
- Contract documented in ut-docs `architecture/wasm-runtime.md`: a `.ask`
  answer must be a pure function of its payload (+ plugin settings, which
  core now invalidates on every write path); real per-instantiation cost
  recorded.

## What the independent review found (all fixed)

1. **BLOCKER — settings-driven answers were pinned.** Both shipped tax
   plugins read a plugin *setting* inside the ask handler (`settings_get`
   host fn); `POST /api/plugins/{id}/settings` performs no `Reload`, so the
   original diff would have kept charging the old VAT rate after a manager
   edited the rate mapping — silently, until an unrelated restart. The
   reviewer proved it with a live test, not just a read. Fixed:
   `BumpGeneration()` on settings save (when anything changed) and in
   `rederiveSettings` (replica drift / cloud directive path);
   regression-tested end-to-end through the real settings endpoint
   (`TestAskTaxRateBP_PluginSettingsSaveInvalidatesCache`, confirmed red
   before the fix).
2. **major — permission revoke no longer took effect.** `Ask` skips
   permission-denied subscribers, but the cache stopped re-asking. Fixed:
   bump on grant and revoke; regression test drives the real revoke
   endpoint (`TestAskTaxRateBP_PermissionRevokeStopsCachedOverride`,
   confirmed red — after fixing the test itself, whose first version
   passed spuriously because `plugins.Init` in the harness bumped the
   generation post-priming; harness now builds before the cache is primed).
3. **minor — unparseable answers were cached as permanent declines.**
   Fixed: answered-but-garbage JSON now declines uncached
   (`TestAskTaxRateBP_UnparseableAnswerIsNotCached`). The pre-existing
   `rate_bp > 0` wrinkle (a literal `{"rate_bp":0}` zero-rating answer
   reads as "no opinion") is unchanged — pre-existing semantics, noted for
   a future contract revision, not silently redefined here.
4. **minor — doc comment claimed the payload was the whole input.** It
   wasn't (settings). Comment rewritten to enumerate the real invalidation
   surfaces.
5. **minor — singleton-bus test hygiene.** `TestAskTaxRateBP_NoSubscribers`
   now resets the shared bus itself instead of depending on another file's
   cleanup ordering.
6. **nit — overflow/concurrency untested.** Added
   `TestAskTaxRateBP_OverflowAndConcurrency` (cache-max+1 distinct
   payloads through the drop-all eviction; 8 goroutines hammering one
   payload; suite run under `-race`).
7. **nit — three untracked strategy docs in the tree.** Excluded from this
   commit (committed nothing beyond the reviewed scope).

Reviewer also verified (running, not reading): build/vet/gofmt clean, all
three packages' tests + full `internal/pages` under `-race`, generation
lock discipline, the reset-during-ask store race is closed, offline-first
preserved (plugin failure → uncached decline → line falls back to its own
rate), payload fields really are derived at recompute time, no SQL outside
the data layer, no real shop names/secrets in test data.

## Verified beyond automated tests

Real Pi4 (192.168.1.167), same catalog/plugin as the field report, isolated
second instance, **after** the review fixes:

- first-ever 10-distinct-item ring-up: 1.4 s total (~140 ms/item — each
  item pays its wasm boot once per process lifetime)
- tender, 10 lines: **27 ms** (pre-fix: 1.5 s at 8 lines, worse at 10)
- repeat 10-item ring-up: 294 ms total (~29 ms/item)
- TDD discipline: every new test observed red before its fix landed (the
  two memoization tests failed "plugin ran 3 times, want 1" pre-cache; the
  three review-driven tests each failed pre-bump/pre-change as documented
  above).

## Verdict

Safe to merge. Deferred (tracked): ut-docs#222's follow-up notes TinyGo
plugin-authoring guidance and a longer-term resident-instance design (ADR
touching ADR-0001); ut-docs#225 (kiosk Chromium accessibility flag);
`{"rate_bp":0}` zero-rating contract wrinkle noted in finding 3.
