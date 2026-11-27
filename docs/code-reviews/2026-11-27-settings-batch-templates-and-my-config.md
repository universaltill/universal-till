# Settings batch: shop-type templates (ut-docs#6332) and my. as the configuration record (ut-docs#6967)

One combined review of the uncommitted settings-batch work in five worktrees:
`universal-till` (this repo), `ut-cloud`, `ut-my-shop`, and two `ut-docs`
branches (ADR text). Cards: epics ut-docs#6332 (ADR-0190) and ut-docs#6967
(ADR-0213, ADR-0214, ADR-0175 sections 16 and 17), with #6823, #6982, #6970 to
#6973. #6986 is schema and ADR text only (blocked on #6985).

## What shipped

- **universal-till, track A.** Shop-type templates as embedded declarative
  data (`internal/shoptemplate`): closed seed whitelist, wizard, preview,
  PIN-confirmed change with a 7-day undo, "More settings" demotion, menu
  launcher, offer card for existing shops. The built-in salon layout plugin is
  retired by a boot step; a legacy `service` shop keeps its layout through the
  template's `legacy_layout`. Registry generation gains `scope`, `restorable`,
  `owner` and the tier gate (`internal/registrydesc`, U1).
- **universal-till, track B.** `internal/pluginsecretapply` no longer has a
  compiled allow-list: the installed manifest, re-verified against the
  marketplace key at apply time (`plugins.VerifiedManifestSettings`), decides
  what a `set_plugin_secret` directive may write. Wire v2 (kind, op, ver,
  base_ver, aux, restore); v1 still applies. Per-field `ver`/`origin` on
  `plugin_settings` (migration 119) and `setting_meta` for core keys
  (migration 118). `internal/restoredsecrets`: the "enter these keys again"
  list after a restore.
- **ut-cloud.** Six ent schemas (C0), the record (`internal/shopconfig`:
  ingest, conflict rule, publish, undo, restore plan and job), generic plugin
  secrets in `internal/providerkeys`, the my. API. All of it behind
  `SHOP_CONFIG_RECORD_ENABLED`, default off.
- **ut-my-shop.** Plugin settings edit form, conflict view, configuration
  page, secret field, restore dialog.
- **ut-docs.** ADR-0190 Accepted; ADR-0175 section 16 (Accepted) and section
  17 (Proposed); Proposed amendments to ADR-0199, 0202, 0213, 0214; contract
  `reference/contracts/shop-config-v1.md` and v2 vectors.

## Review (independent; combined Reviewer on Fable, three Opus 5.5 sub-reviewers)

The Reviewer read the secret and payments-adjacent code itself (till apply,
manifest re-verification, migrations, cloud `providerkeys` plugin secrets,
restore redelivery, secret and restore handlers, ingest, publish). Three
read-only sub-reviewers on a different model covered track A in the till, the
non-secret cloud record and API, and my. plus the ADR text. Every finding below
was checked against the code before it was kept.

### Security checks that held

- **No secret value stored, logged or returned.** Till: refusals are reason
  codes, the audit row carries plugin, key, ver and directive id only. Cloud:
  the record keeps a value only when `StoresValue` allows it (secret tier,
  `report:false`, credential-named and descriptor-less keys keep "set" at
  most); the 409 of a secret carries the till's `ver` only; restore plan,
  job row, checklist and audit rows name fields only.
- **Manifest-driven apply re-verifies the signature.** Signature and package
  digest are checked at apply time; an edited manifest is terminal
  `not_allowed`, an unreadable one retries, a path outside the plugin
  directory is refused.
- **Step-up and roles.** Secret write, clear and "remove our copy": owner plus
  step-up. Restore start and cancel: owner plus a fresh step-up; a dry run is a
  view. Viewer 403, another merchant's store 404, the store always from the
  authenticated route.
- **Restore redelivery.** Retention (a) is never redelivered. Retention (b)
  only with this restore's `include_secrets`, the `key_read` audit row
  (purpose `restore_delivery`) written before the read, sealed fresh to the
  target till's directive key. Fiscal and payment-identity keys are never
  replayed (`NeverRestored`).
- **Flag.** `SHOP_CONFIG_RECORD_ENABLED` defaults to off: routes 404, ingest
  stores nothing, no sweeper, the secret reconcile does not run.
- **Templates.** The seed whitelist is a closed literal list with deny
  prefixes, checked at load, apply, diff and confirm; no payment, fiscal, tax,
  PIN, role, currency, retention or secret key is seedable; no UK eat-in VAT
  seed (legal ut-docs#6290). Cashiers get 403 on every template route;
  satellites 409.
- **Migrations.** 118 and 119 are new files, no shipped migration was edited,
  both checksums are pinned and `TestShippedMigrationsUnchanged` passes.
- **Cloud schema.** The six tables are regional (residency guard passes) and
  are deleted by tenant erasure with rows present.

### Findings fixed in this review

| # | Repo | Severity | Finding | Fix |
|---|---|---|---|---|
| F1 | till | major | Undo of a shop-type change wrote `sale.order_type_prompt` back to unset but skipped `InitOrderTypePromptMode`, so the sale screen kept not asking eat-in or takeaway until a restart. | `shop_type_change.go`: always re-init (empty falls back to `top`). Test now asserts the live mode; seen red on the old line. |
| F2 | till | minor | Undo wrote back whatever keys the stored undo record named, without the seed whitelist. | Undo skips any key that is not `shoptemplate.IsSeedable`. |
| F3 | till | minor | The salon plugin was removed at boot even when the templates failed to load (no replay of its layout). | `init.go`: removal only when `shopTemplates != nil`. |
| F4 | cloud | major | "Remove from tills" recorded the field as set to an empty string, so a later restore sent `op:set ""` (refused for a choice field, wrong for the rest). | `shopconfig/store.go`, `publish.go`: a clear is stored with no value and `value_set` false; a restore replays nothing for it. Test extended, seen red. |
| F5 | cloud | minor | A signed manifest key containing a vertical bar or longer than 128 characters became a record key and a directive entity id that later parsed wrongly or failed with a 500. | `shopconfig/descriptor.go`: such a key has no descriptor (fail closed). New test, seen red. |
| F6 | my. | major | The restore dialog kept `include_secrets` after "Try again" and after picking another till, and restarted on the summary with the old plan. | `RestoreConfigDialog.tsx`: restart goes to the till choice with the approval off and the plan cleared; a till change clears the approval. Test updated. |
| F7 | docs | blocker | The ADR-0213 amendment read as "kept keys go to a new till only with the owner's approval" while accepted Decision 4d delivers the SumUp key automatically, and the code still does. | Amendment scoped to the restore job and generic plugin secrets; Decision 4d stated as unchanged; the open question is on the owner's decision sheet. Same clause in ADR-0175 17 and the contract. |
| F8 | docs | major | ADR-0175 17, ADR-0199 and the contract promised a replay limited to fields newer than the backup; that is not built. ADR-0199 named a till-initiated redelivery no contract defines. | Reworded to what is built (a value the till holds itself is never overwritten); the till never requests a secret. |
| O8 | till | major | A salon layout the manager had switched off came back after upgrade (the boot removal ignored the inactive state, then `legacy_layout` replayed it, hiding Tables and Kitchen stations). | `retired_layout_plugin.go`: an installed but inactive retired plugin writes `shop.legacy_layout_off` (shop-wide) before removal (kept for next boot if the write fails). `TemplateState.LegacyOff` silences `legacy_layout` until a template is accepted (ledger). Tests: `TestLegacyLayoutOff_SilencesLegacyUntilAccepted`, `TestInit_DisabledRetiredSalonPluginKeepsFullMenu` (active and inactive). |
| O9 | till | minor | The main till accepted `shop.type` / `shop.template_*` from a satellite on the previous build. | `sync_settings.go`: those keys are refused with 409 `main_till_only`. New builds already never forward them. Test `TestSyncSettingsApply_RefusesShopTypeAndTemplateKeys`. |
| O12 | till | minor | Apply read the local ver and wrote in two steps. | `PluginRepo.SavePluginSettingsBatchChecked` runs the version rule inside the write transaction (`pluginsecretapply.CheckedSaver`); the early check stays for a cheap refusal. Tests: `TestApply_TillEditBetweenCheckAndWriteIsNotOverwritten`, `TestSavePluginSettingsBatchChecked_GuardAbortsInsideTx`. The untested primary-till guard in `cloudSetPluginSecret` is still open. |
| F9 | docs | major | `shop-config-v1.md` had drifted: undo route, version floor of 2, restore roles and the dry run, the cancel route, checklist kinds, the secret 409, owner-only secret writes, `op` missing from the v2 wire in ADR-0175 and ADR-0214. | Corrected against the handlers. |
| F10 | docs | major | The ADR-0202 amendment said Decisions 1 to 7 were unchanged while its Receive default (Ask) turns an ordinary inbound change into a Review question again. | The amendment states the changed default openly and stays Proposed until the owner approves it. |

### Findings open (must be closed before the named gate)

Before `SHOP_CONFIG_RECORD_ENABLED` is switched on for any shop:

- **O1, major, cloud and till: equal-version collision.** The cloud mints ver
  N+1 while the till, offline, makes its own edit N+1. The till refuses the
  cloud directive (`conflict`, correct), but the cloud ingest ignores the
  till's report because the ver is equal (`ingest.go`, `cd.ver <= cur.Ver`),
  so the record keeps a value the till never applied and no conflict is shown.
  If the manager saves once more before the till reports, the next directive
  (base N+1) passes the till's rule and overwrites the till's own edit. This
  contradicts ADR-0175 17.5 ("equal or unknown: conflict view, never a coin
  flip"). The same shape exists for a kept secret
  (`ReconcileTillReport`, `r.Ver <= st.Ver`). Needs: ingest to take an
  equal-ver report whose origin is `till` over a `cloud` row, and the directive
  to carry the base origin (or a value fingerprint) so the till can refuse;
  contract and vector change. Not a trivial fix, not done here.
- **O2, minor, cloud.** No test runs two edits concurrently; on Postgres two
  first edits of a never-seen field end in a unique-index 500 instead of a
  409, and a lost race answers with the stale row.
- **O3, minor, cloud.** Undo can publish an empty string when the newest
  different revision kept no value (`UndoTarget` ignores `HasValue`).
- **O4, minor, cloud.** The revision sweep stops when the flag is switched off
  after use, so revisions outlive 30 days.
- **O5, minor, cloud.** `app_endpoints_shop_config.go` reads directive times
  itself for `status[].at`; the "no second derivation" guard scans only
  `internal/shopconfig`.
- **O6, minor, cloud.** A restore with `include_secrets` overwrites a secret
  the owner already typed on the new till when that till's ver is not above
  the cloud's; skip a secret the target reports as set with origin `till`.
- **O7, minor, cloud.** `manifest-render-sweep.sh` flags fewer credential-like
  keys than `claims.SecretLikeKey`.

Before the till branch is pushed:

- **O8, major, till: a salon layout the manager had switched off comes back.**
  The old sync kept a disabled salon plugin off. The boot step now removes it
  regardless and a `service` shop with no ledger renders the legacy salon
  layout, so Tables and Kitchen stations disappear for a shop that chose the
  full menu (against ADR-0190 Q1, never forced). Needs a marker written before
  the removal when the plugin was inactive, and a test.
- **O9, minor, till.** The main till still accepts `shop.type` and
  `shop.template_*` from a satellite on the previous build through settings
  sync; the new type's template then applies with no preview (mixed-version
  window, ADR-0086).
- **O10, tester defect 4, still open.** `guard-docs-shots.sh` fails until the
  screenshots are regenerated on Linux (never hand-edit the manifest).
- **O11, nit.** The legacy Settings golden was re-captured after the plugin
  was deleted, so for that one page "byte-identical" is not proven by the
  golden; menu, admin and rail goldens are real. Market stall tucks the idle
  lock card under More settings (visibility only). Clearing the type has no
  undo.
- **O12, minor, till.** The apply reads the local ver and writes in two steps;
  a till edit in between is overwritten (single till, check-in time, small
  window). The pre-existing primary-till guard in `cloudSetPluginSecret` has no
  test (the blob is sealed to the main till's key, so this is a second line).

Before the my. branch is pushed:

- **O13, blocker, my.: manual.** The new restore page has no help topic;
  `help/en/plugins.md` still says secrets have no Edit; the (a)/(b) choice is
  documented nowhere (#324).
- **O14, major, my.** The retention choice can never appear: the form reads
  `secret_choice` from the plugin schema, which the cloud does not send; the
  contract puts it in `GET /config` as `secret.choice`. The owner cannot pick
  (b). The test fixture carries a field the server never sends.
- **O15, major, my.** A 409 on a secret save maps to the generic error and
  keeps the stale ver, so every retry fails until a reload.
- **O16, major, my. and cloud.** Money settings assume two decimals: the
  plugin settings view carries no currency or decimals.
- **O17, minor, my.** Secret states do not match the contract (a failed
  replace reads "Saved"); a second edit before a reload self-conflicts; five
  error codes fall to the generic message; unknown reason codes render as raw
  keys; core enum settings render as free text; plugin label keys render raw
  on the configuration page; a manifest `pattern` is compiled in the browser.

#### my. and cloud: fixed after the review (dev-fixes3, 2026-11-27)

| # | Status | What was done |
|---|---|---|
| O13 | fixed | New help topics `help/{en,de,tr}/restore.md` (node `config/restore`); `help/*/plugins.md` now describes the secret flow (owner, sign-in, (a)/(b) choice, Remove our copy, Remove from tills, changed-on-till) and no longer says secrets have no Edit or that keys never leave the till. The cloud help index re-pin (`go run ./cmd/helpindex -my ../ut-my-shop`, plugins is allow-listed) is to run once the my. branch is committed. |
| O14 | fixed (my. side was wrong) | The cloud sends `secret.choice` in `GET /config` (contract-B3); my. read `secret_choice` from the plugin schema. `parseSecretInfo` now reads `choice`; `SecretField` uses it first (schema flag only as fallback). Page test fixture no longer carries a schema field the server never sends; tests seen red first. |
| O15 | fixed | A 409 `conflict` on save or clear reads the till's `ver` from the body, refreshes the row (state "changed on the till", ver = till's), shows `manage_app.secret.error.conflict`, and Enter again sends `base_ver` = the till's ver (page test: second PUT sends 9, not 5). The body is read for the integer only. |
| O16 | note only | The plugin settings view carries a shop-level `currency` and `currency_decimals`, and my. formats money with them; the descriptor has no per-field decimals field. Left as is. |
| O17 (secret part) | fixed | States map per contract-B3 (applied = saved, none/cleared = not saved, `failed` + `fail_reason` shown with the existing reason text and offers Enter key); error codes `conflict`, `no_main_till`, `plugin_not_installed`, `not_configured`, `no_cloud_copy`, `provider_form` and 422 `invalid_value` have their own sentences; the clear / remove-copy path uses the same mapping. New keys added to ut-cloud `locales/*.json` (10 files, English text) and `english-only-until-weblate.txt`. |
| O17 (rest) | remaining | A second edit before a reload self-conflicting (plain fields), core enum settings as free text, plugin label keys raw on the configuration page, manifest `pattern` compiled in the browser. |

Gate: my. `npm test` (4229), lint, typecheck, `guard-cloud-keys` OK; cloud `go test ./internal/api ./cmd/helpindex ./internal/helpdocs` pass.

Docs, not blocking: ADR-0175 16.1 says `shop.type` is both tier `sensitive`
and in `never` (pick one); section 17.2 says scope `register` where 15.2 says
`device`; three paths under `temp/` are cited from ADR text.

### TDD claims re-verified by the Reviewer

Done with build overlays (a mutated copy compiled in place of the file, the
worktree untouched), each run red with the claimed test and green unmutated:

- till: conflict rule, stale-version rule and kind check removed, each fails
  `TestApply_SharedVectorsV2`; manifest verification bypassed fails
  `TestVerifiedManifestSettings_ChangedManifestIsRefusedNotRetried` and
  `TestCloudSetPluginSecret_V2PlainThroughVerifiedManifest`.
- cloud: no approver check fails
  `TestRestorePluginSecret_RefusesWithoutApprovalOrCopy`; no audit before the
  read fails `..._KeptCopySealedFreshToTheNewTillWithRestoreFlag` (0 `key_read`
  rows, want 1); role and step-up removed from the secret route fails
  `TestSecret_RolesStepUpAndNeverTheValue`; step-up removed from restore start
  fails `TestRestore_StartNeedsFreshStepUpAndThePlan`; flag default flipped and
  ingest ungated fail `TestShopConfig_FlagOffStoresNothingAndReadsRefuse`;
  `include_secrets` ignored fails
  `TestRestoreJob_SecretsOnlyWithThisRestoresApprovalAndAIsNeverRedelivered`;
  the till-changed rule removed fails
  `TestRestoreJob_ATillValueOfItsOwnIsNeverOverwritten`.
- One mutation survived: dropping the retention check in
  `RestorePluginSecret` fails nothing, because a retention (a) field has no
  stored copy and the missing copy already refuses. The check is a second line
  with no test of its own.

### Runs

Sub-reviewers: `go build ./...` and `go vet` clean in till and cloud; the
track A, registry, migration and shopconfig packages pass, `-race` on
`shoptemplate`, `uislot`, `settingsnav`, `pages/common`; my. typecheck, lint
and the full vitest run (183 files, 4219 tests) pass; `guard-adr-index.sh`
passes on both docs branches; the three vector files are byte-identical.

Gate after the fixes (Reviewer): till `go test ./internal/pages` (full, 410 s),
`shoptemplate`, `pluginsecretapply`, `restoredsecrets`, `data`, `db` pass;
gofmt and `go vet ./internal/pages` clean; guards fixed-dates, data-access,
i18n, i18n-meta, visibility-class, core-neutral, deadcode-baseline pass. Cloud
`go test ./internal/api` (full, 119 s), `shopconfig`, `providerkeys`,
`httpapi/...`, `claims`, `tenanterasure` pass; gofmt and vet clean. my.
`RestoreConfigDialog.test.tsx` (24 tests), typecheck and lint pass. Not run by
the Reviewer: the full e2e suite, ut-cloud `scripts/ci/verify.sh` end to end,
the full my. vitest run after F6, Postgres, the `internal/plugins` full suite.

Pre-flight: till user-visible behaviour `updated` (help topics `display`,
`users`, `menu`; docs-shots `deferred` to the Linux regeneration, O10);
`en.json` keys `updated` (meta present, guards pass); my. UI locales `updated`
(10 locales, parity passes), my. help `deferred` (O13, blocker); ADR index
`updated` (guard passes), reference contract `updated` (F9); plugin manifest
field or host function `n/a` (none added; `layout.shop_type` capability left
in the closed set on purpose, decisions G5-2); plan, price or tier `n/a` (the
restore gate reuses `cloud_backup`, no price or plan text changed); review
record: this file.

## Verdict

- **ut-docs (both branches): go.** Commit A (ADR-0190, 0175 section 16)
  first, then B; the tail conflict in ADR-0175 resolves by keeping 16 then 17.
- **ut-cloud: go to commit**, flag off. O1 must be fixed before the flag is
  switched on anywhere (also owner O4 and Legal ut-docs#6429). The mirror test
  `TestTillSettingsRegistryMirrorsPOS` needs the till PR merged first.
- **universal-till: go to commit; push only after** O8 is fixed and the
  docs-shots are regenerated (O10). The secret apply path is sound.
- **ut-my-shop: no-go** until O13, O14 and O15 are fixed (O16 with the cloud).

## Deferred, to be carded

O1 to O7 (one card, "before the record flag goes on"), O9, O11, O12, O16, O17,
the docs nits, and the owner question on ADR-0213 Decision 4d
(`temp/settings-batch/decisions.md`, Reviewer section).
