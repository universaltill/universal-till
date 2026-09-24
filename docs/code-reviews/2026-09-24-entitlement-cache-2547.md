# Review — till entitlement cache + EffectivePlan (ut-docs#2547, ADR-0060 part b, backend)

- **Date:** 2026-09-24
- **Branch:** `feat/2547-entitlement-cache`
- **Author:** Opus 5.5 (pipeline, lane:cloud-24) · **Reviewer:** Fable (independent, different model)
- **Card:** universaltill/ut-docs#2547 · **Cloud side:** ut-cloud#185

## What shipped
- `internal/cloudsync`: decodes the optional `data.entitlement` block of `POST /v1/stores/sync`; valid → the four `entitlement.*` settings keys in one transaction (`last_confirmed_at` = till clock); absent/null → untouched (old cloud); unknown plan/status or wrong JSON types → ignored whole with a warning; a persist failure never fails the tick or directives.
- `internal/entitlement`: keys, plans, capabilities + `Allows` (mirrors ut-cloud `internal/subscription`), `Grace = 7 days` (ADR-0060 §5), `EffectivePlan(ctx, reader, now)`. `expires_at` is display-only and never evaluated.
- Never a sale gate: package-level import walk (`internal/pos`, `internal/fiscal`, `internal/print`) + file-level check on the sale/EOD handlers in `internal/pages`, each with a positive control; offline sale with a lapsed, 30-day-stale cache and dead network completes.
- No UI, no locale keys — ADR-0060 §6 chip/banners are ut-docs#2569.

## Findings
| Sev | Finding | Outcome |
|---|---|---|
| major | Import guard missed the sale path's HTTP layer (`internal/pages` pos/refund/sync/EOD handlers), where a future `EffectivePlan` check could gate a sale undetected | **Fixed**: `TestSaleHandlerFilesNeverImportEntitlement` + positive control; proven to fail when `pos_api.go` imports the package |
| minor | Unparsable display-only `expires_at` rejected the whole block → no refresh → paid tills degrade after 7 days | **Fixed**: stored empty, plan/status kept (`TestBlockValuesUnparsableExpiresIsDropped`, `TestSyncEntitlementBadExpiresStillRefreshes`, seen failing first) |
| minor | Future-dated `last_confirmed_at` honoured indefinitely | **Fixed**: symmetric bound — more than Grace ahead → local (boundary cases in `TestEffectivePlan`, seen failing first) |
| minor | Offline-sale test's doc overstated coverage (fiscal/receipt/EOD) | **Fixed**: comment now says what it drives; the rest is the structural guard |
| minor | Warning logged every tick while the cloud sends a bad block | Accepted — the cloud validates at the source (ut-cloud#185 ent `Match`) |
| nit | `go/build` applies host build constraints (a `_windows.go` import is invisible on Linux CI) | Accepted — no GOOS-specific files in those packages |
| nit | `time.Now()` instead of `internal/clock` in cloudsync | Accepted — matches the surrounding tick code |

## Verified beyond the author's tests
- Reviewer re-ran TDD claims: removing the `cacheEntitlement` wiring fails the three present/null/lapse tests; `>` → `>=` in the grace check fails the exact-boundary case.
- `internal/procrestart TestRestartSchedulesDelayedReexecOfOwnExecutable` failed once in the author's full run under load; 5/5 passes with `-count=5`, package untouched by this diff — pre-existing timing sensitivity, not this change.
- Gate after fixes: `gofmt -l .` empty, `go build`, `go vet`, full `go test ./...` green, `golangci-lint` 0 issues, `guard-data-access.sh`, `guard-i18n.sh`; author ran the other build-job guards (58/59; `guard-shellcheck-version.sh` needs shellcheck, not installed here — CI runs it).
- No visual surface touched.

## Round 2 — CI `desktop-shell` (deadcode guard)
CI failed: `guard-deadcode-baseline.sh` flagged `EffectivePlan`/`Allows` as unreachable (tests were the only callers). Not baselined — gave them their genuine consumer instead: manager-gated read-only `GET /api/entitlement` (`internal/pages/entitlement_api.go`; `canPerform(d, r, "settings")`, existing i18n keys, `{data,error}`), the read surface ut-docs#2569's UI builds on. Independent Fable review of the delta: no blockers/majors; auth middleware 401s anonymous callers and the kiosk exemption can't reach it. Minor taken: `TestEntitlementAPIIsNotExempt` pins it outside `exempt()`/`optionalAuth()`. Nits accepted (7 settings reads per call, off the sale path; `slices.Sort`). Handler tests (no session/cashier 403, fresh shop, stale → local, no keys → local) seen failing first.

## Verdict
Safe to merge after ut-cloud#185 (either order is wire-safe per ADR-0060 §3).

## Deferred
- ADR-0060 §6 chip/banners, settings plan display, help topic → ut-docs#2569.
