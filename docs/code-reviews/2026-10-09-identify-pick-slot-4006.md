# Review: catalog.identify pick route + pending-confirm photo slot (ut-docs#4006)

- **Card:** ut-docs#4006 (ADR-0121 amendment 2026-10-09, R2a). Built by Opus 5.5; independent review by **Fable**.
- **Branch:** `feat/4006-identify-pick-slot`; docs: ut-docs `docs/4006-identify-pick-slot`.

## What shipped

- `internal/plugins/wasm_upload.go`: **kept uploads** (`StageUploadKept`, `TakeUpload`). The guest's `upload_close` on a kept token consumes the token but parks the file, so core can still take it when the job ends. `ReleaseUploads`/`CloseAll` delete parked files; parked files count toward the 8-per-plugin cap. Design gap found in BA: without this, a well-behaved plugin's `upload_close` would delete the photo before R2a could move it.
- `internal/pages/plugin_job.go`: `startPluginJob` takes an `onDone` hook, run before `finish` and the deferred `ReleaseUploads`.
- `internal/pages/identify_slot.go` (new): one pending-confirm slot per plugin `{job_id, path, type}`; a new capture replaces it (old file deleted). Expiry is `pluginJobUnpolledTTL + 120 s` from the result, reset to 120 s at hand-out. A slot is taken once, by job id.
- `internal/pages/plugin_identify.go`: the photo is staged kept; a valid result (no error, a document, no redirect, job still live) moves it into the slot; the poll marks hand-out; `identify_result` carries the job id. New `POST /api/pos/identify/plugin/pick` takes a form body only, sets `code=sku`, runs **the `/api/pos/scan` handler itself**, and only then, on a `RecoverAndLog` goroutine with `context.WithoutCancel`, does it learn. The learning step takes the slot, resolves sku via `Engine.ResolveBase` (core resolves the item id), reads at most 8 MiB, runs bounded `imaging.Decode` (PNG/JPEG only), stores through `itemimages.StoreAIRef`, nudges `ScopeAdmin` and audits `ai_identify_confirmed`; the slot file is always deleted.
- `internal/itemimages.StoreAIRef`: the store step (paths-based dir, `MkdirAll`, `O_EXCL` nanosecond name with a bump for coarse clocks, partial-file removal, prune to 5). The built-in `/api/pos/identify/confirm` now calls it too (moved, not copied).
- `pos_api.go`: the scan closure is lifted into `scan` byte-for-byte; the pick route is registered beside it. `demo_mode.go`: the pick route is demo-denied.
- Template `identify_suggestions.html`: buttons post `{_job, qty, sku}` to the pick route; markup otherwise unchanged (overlay close keys off `data-identify-pick`). Help `sell.md` in en/de/tr/ar/fa says the picked photo is kept as a reference photo (newest five, also on linked tills).

## Findings (Fable)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor (treated as must-fix) | A JSON body skipped the form: scan's JSON branch read its own `code`, so the pick added item B and stored the photo as item A's ai_ref (reproduced). | **Fixed:** pick refuses `application/json` with 400; `TestPluginIdentify_PickRefusesJSONBody_4006` (failed 200 before the fix). |
| 2 | minor | `onDone` could fire for a job `cancelRunning` already dropped (answer in flight) and replace the newer job's slot. | **Fixed:** `keep` checks `pluginJobs.owner(id, identifyRoute)`; `onDone` doc says so. |
| 3 | minor | No pages-level test for "plugin calls upload_close, photo still stored": reverting `StageUploadKept`→`StageUpload` leaves pages tests green. | **Accepted:** the in-process test plugin cannot call the WASM `upload_close`; the guard is `TestKeptUpload_CloseParksTakeHandsOver_4006` (registry level, covers close→park→take, release/unload delete). The end-to-end run belongs to #2851 (first real plugin), noted on that card. |
| 4 | nit | `handedOut` could reset an already-fired timer if the pre-hand-out TTL were shorter than the job's result TTL. | **Fixed:** documented and pinned by `TestIdentifySlotTTLOutlivesJobResult_4006`. |
| 5 | nit | An unfetched result's photo lingers 135 s rather than 30 s. | Accepted: bounded (one ≤ 8 MiB file per plugin). |
| 6 | observation | `TestSetupWizard*` TempDir cleanup flakes, pre-existing. | Filed **ut-docs#4015** (Triage). |

Also caught by the full suite before review: the `go learnIdentifyPick(...)` statement tripped `TestNoUnrecoveredGoroutines`. Fixed by putting `RecoverAndLog` in the `go func` literal.

Reviewer verified OK: no slot-file leak on any path (take, expire, replace, failed job, unload; crash → 24 h `ut-view-upload-*` housekeeping); scan behaviour unchanged; `code` can't be smuggled past `sku`; Windows-safe removal order; `StoreAIRef` MkdirAll + `paths`; route session-gated and demo-denied; the four translations match the English.

## Verification

- TDD: each new test failed first (build failure, then behaviour). Mutation checks: making the keep hook a no-op fails `PickAddsLineThenStoresPhoto` (`pending-confirm photo files = 0, want 1`); the reviewer independently re-ran this (5 tests fail).
- `go build ./...`, `go vet`, `gofmt` clean. Full `go test ./...` green (after the goroutine fix); identify/job/view/demo/AI tests green under `-race`.
- CI guards run locally: all pass except three that fail on this Mac for environment reasons and also fail on `main`: `guard-deadcode-baseline` (GTK-tagged logging funcs), `guard-shellcheck-version` (0.11 installed), `guard-store-private` (BSD awk multibyte). Local `golangci-lint` is built with an older Go than the repo targets, so CI runs it.
- **Not verified:** a browser-driven run. No plugin answers `catalog.identify` yet, so nothing can drive this seam on a real till. The visible markup is unchanged apart from the post target. The driven run is required on #2851 (comment posted there).

## Verdict

Safe to merge.
