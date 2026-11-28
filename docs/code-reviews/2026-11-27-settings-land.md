# Review: settings-land (merge of origin/main into the settings batch, till + cloud + my)

Date: 2026-11-27. Reviewer: the one combined Reviewer for the batch. Scope: only what is new since
the substantive review (`2026-11-27-settings-batch-templates-and-my-config.md`).

## Verdict

- till: **APPROVE** (with note 3: origin/main has moved again).
- my: **APPROVE**.
- cloud: **BLOCKED until one fix** (finding 1): the in-progress merge duplicates a registry key and the
  `claims` package panics at init.

## Findings (ranked)

1. **IMPORTANT, cloud, merge result broken: duplicate `payments.tips_enabled` in
   `internal/claims/tillsettingsregistry.json`.** HEAD already carries the re-pin (4b22e5c5) with the tip
   settings; the origin/main side added an older copy of the same entries, and the merge staged both
   (keys at lines 168 and 222). `go test ./internal/claims/` fails with
   `panic: claims: till settings registry: duplicate key "payments.tips_enabled"` at package init, which
   would crash anything importing `claims`. Fix: `git checkout HEAD -- internal/claims/tillsettingsregistry.json`
   (verified: the HEAD copy is byte-identical to till `docs/till-settings-registry.json`, so the pin mirrors
   till exactly), re-run `go test ./internal/claims/`, then commit the merge. Not fixed by me (instructed
   record-only).
2. **NOTE, cloud:** `internal/helpdocs/data/index.json.gz` is in the merge but must be regenerated from the
   merged till `web/help` (till is now final), otherwise the pin and the index drift. `helpdocs` tests pass
   on the staged copy; regenerate and re-run. `go build ./...` and `cryptoseal` tests pass.
3. **NOTE, till: `origin/main` moved after the merge** (now efe0d37a6: TSE credential marker #7050,
   enroll-report leak #7153). Land/till is merged up to 1fccafd20 only. The `pending` push needs a merge of
   those (a conflict is not expected to touch settings files, but `init.go` / `pos_api.go` were the hot spots,
   so re-run build after it). If the push is rejected as non-fast-forward or the PR shows `dirty`, merge once more.
4. **PASS, till e2e PIN fix:** `template-cashier-6332.spec.ts` PIN 135792 -> 580264. 135792 is
   `KIOSK_PIN` in `session-kiosk-pin-mode-3136.spec.ts`; `notices-cashier-5366.spec.ts` already documents the
   same trap. 580264 collides with no other PIN in `e2e/`. Test-only, correct, comment explains why.
5. **PASS, till merge semantics:** migrations on main end at 119, ours add 120_setting_meta and
   121_plugin_settings_ver (unique, contiguous; only these two are new vs main; `internal/db` tests incl.
   drift/checksum pass, 108s; `internal/data` passes). `en.json` (5200 keys) and `en.meta.json` (1874) parse with
   no duplicate keys. `init.go`: main's content kept, batch replaces the shop_type builtinlayouts boot sync and
   `StartShopTypeLayoutReconcile` with template reload + `StartTemplateStateReconcile` as in the substantive
   review (guarded so a failed template load keeps the salon layout). `go build ./...` clean, `go vet` on
   pages/db/data clean. `web/help/img/manifest.json` valid JSON, regenerated; no conflict markers anywhere
   (only licence-file `=====` rules).
6. **PASS, my `src/index.css`:** union is clean. Main's side loses no line; the only lines removed vs the
   pre-merge batch file are the two intentionally edited `.pset__action` rules. Brace count balanced
   (2303/2303), no markers, the Shopify `integ-` block (58 refs) and the batch blocks (`pset__edit`,
   `pset__secret`, `cfg__`, `rc__`) are all present, no duplicate selectors with conflicting values introduced.

## Rules checked

SumUp: no commission touched. Flags: `SHOP_CONFIG_RECORD_ENABLED` unchanged (still default off, no config/infra
file sets it). No Stripe key. No real shop names in the new test data.

Pre-flight: help topic + docs-shots manifest updated (n/a for the PIN change); en.json -> en.meta.json updated
(English only); my. UI locales n/a (merge only); review record: this file.

## Deferred

None; finding 1 must be fixed before cloud is pushed, finding 2 follows till's final state.
