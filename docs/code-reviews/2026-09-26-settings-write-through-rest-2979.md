# Review: remaining shop-wide settings writes go through the main till, plus a CI guard (ut-docs#2979)

- **Date:** 2026-09-26 · **Lane:** `lane:cloud-54` · **Complexity:** hard
- **Built by:** Opus 5.5 (dev subagent) · **Reviewed by:** Fable (independent subagent, detached worktree)
- **Precedent:** ut-docs#2791, ut-docs#2948 (`2026-09-26-savestate-settings-write-through-2948.md`).
- **Filed from this work:** ut-docs#2997 (EOD business day, report retention, auto-update schedule), ut-docs#2998 (replica cloud directives, setup/TSE paths), ut-docs#2999 (guard blind spot: writes that bypass `.Settings`).

## What shipped

On an additional till, these writes used to be saved locally and then silently reverted by the next admin pull. They now go through `saveShopSettings` / `saveStateThrough`. Each change travels as one batch, a refusal writes nothing and leaves no audit row, and a main till behaves as before:

- **Receipt designer:** the 8 `receipt.*` keys. A refusal answers with a fragment. A local store error is now reported instead of being ignored.
- **Invoice seller details:** the 3 `invoice.*` keys, sent with the handler's elevation.
- **Fiscal device confirm/unpair:** `fiscal.signing_device_configured.<cc>`. The main till re-checks the owner-only `fiscal_tse_override` against its own staff list. The sale-time auto-confirm in `fiscal_device_hook.go` stays local (offline-first) and is a reviewed exception.
- **Printer card:** the `printer.*` keys are per-till, so they are still written locally. They now go as one batch, and errors are surfaced.
- **Import currency confirmation:** the switch and `store.currency_confirmed` go in one batch. `SetState` and `InitCurrency` run only after the save succeeds. This path can't be reached on an additional till today, because imports are refused there (#1696); a test pins that.
- **Barcode types:**
  - An additional till computes the new set with the new `data.NextBarcodeSymbologySet` and sends it. A main till keeps the atomic toggle.
  - `SettingsRepo.Set/SetMany/Delete` and `ApplyAdmin` now invalidate the symbology cache. Before this, a set changed by the admin pull stayed stale until a restart.
- **Guard:** `internal/pages/settings_write_guard_test.go` scans `internal/pages` with go/ast. It flags `.Settings.Set/SetMany`, `common.SaveState` and the barcode setters unless one of these holds:
  - the key resolves to a per-till constant;
  - the line carries `// settings-write:allow <reason>`;
  - the file is on a 4-file main-till-only allowlist.
- **Help:** step 13 of the `multitill` topic, in all five languages, now lists the barcode types, receipt design, invoice business details and fiscal device confirm/unpair. The docs-shots manifest hashes are refreshed. No pixels change.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The guard sees only `<x>.Settings.*` receivers. `fiscal_api.go`'s signing override (`store := dp.Settings; store.SetMany(...)`) and `locale_generation.go` write shop-wide keys around it. The replica signing override also needs a product decision, because the main-till apply refuses non-empty overrides. | **Filed ut-docs#2999.** |
| 2 | minor | Barcode types on an additional till send the full set computed from the local copy. A toggle made elsewhere between pulls loses to the last writer. | Accepted: the setting changes rarely, and the method comment documents it. A per-id delta on the apply endpoint could follow. |
| 3 | nit | On a main till, a currency switch through import writes `store.currency_confirmed` twice. | Accepted: harmless and idempotent. |
| 4 | nit | With `UT_AUTH=off`, the actor is empty, so an additional till gets "main till refused". | Accepted: dev mode only, same as #2791. |
| 5 | nit | On a main till, store errors that the printer, receipt and invoice handlers used to swallow now answer 500, and the writes are one transaction. | Intended. |
| 6 | nit | No test pinned the main till's owner-only refusal for fiscal confirm. | **Fixed:** `TestSettingsWriteThrough_FiscalDeviceMainRefusesNonOwner`. It checks for a 403, that the main till wrote nothing, that nothing was written locally and that there is no audit row. |

The reviewer checked each of the following and found no problem:
- elevation and actor handling: no approver is faked, and the main till re-checks permissions with its own rules;
- no partial writes, and no audit row on failure, in all six handlers;
- barcode cache lock ordering: the lock is never held across DB calls;
- import ordering;
- the help text in all five languages;
- i18n: no new keys;
- the recurring `MkdirAll` / cwd-path bugs: none found.

## Verification

- **TDD red (dev and reviewer):**
  - With the receipt, invoice and fiscal handlers reverted to `main`, their six tests fail: "main till calls = 0, want 1", and on an unreachable main till the old code answered 200/204/303 with a local write instead of 502. Restored, they pass.
  - An injected `d.Settings.Set(..., "receipt.footer", ...)` makes the guard fail, naming `receipt_designer.go:135`.
- **Gate:**
  - `gofmt -l .` empty, `go build ./...`, `go vet` clean.
  - `go test ./...` green; `internal/pages` under `-race` needs `-timeout 45m` and passes.
  - `golangci-lint` 0 issues.
  - Guards pass: data-access, i18n, help-topics, help-drift, docs-shots, core-neutral, compliance-claims, competitor-naming, page-http-error, kiosk-engine, demo-env, readme-local-links and the rest of the build job's list.
  - `guard-deadcode-baseline` could not be judged locally: the GTK headers are missing, so `cmd/unitill-desktop` is skipped. That is a sandbox artefact in files this change doesn't touch; CI runs it with the headers.
- **Not done:** no two-till browser run. As in #2791 and #2948, the flow is covered end to end by httptest against the real main-till handler on its own migrated DB. Nothing visible changes except refusal messages that already exist. UX: no template changes; the error shapes are the ones the neighbouring handlers already use.
- **Language packs:** no new locale keys.

## Verdict

Safe to merge. Deferred: ut-docs#2997, #2998, #2999.
