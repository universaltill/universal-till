# Code review — `internal/fiscal`'s "TSE" vocabulary renamed to "signing device" (ADR-0081, ut-docs#1587)

- **Date**: 2026-09-07
- **Repo**: `universal-till`
- **Branch/PR**: `fix/1587-fiscal-signing-device-rename`
- **Card**: universaltill/ut-docs#1587, `complexity:hard`
- **ADR**: ADR-0081 (amends ADR-0048's identifier names only; its gate *policy* is untouched)
- **Written by**: Fable. **Reviewed at**: Opus, cold — the reviewer did not see the
  implementer's reasoning, only the diff.

## What shipped

A pure-naming change across `internal/fiscal` and its call sites: Germany's legal
"TSE" vocabulary replaced with market-neutral "signing device", because the gate
already covers Turkey (YN ÖKC, ut-docs#1208) and every further ADR-0047 market
inherits whatever name is chosen now.

1. **Settings keys** — `fiscal.tse_configured` → `fiscal.signing_device_configured`,
   `fiscal.tse_failing_since` → `fiscal.signing_device_failing_since`, and
   `fiscal.tse_override_{until,reason,actor}` → `fiscal.signing_override_{until,reason,actor}`.
   Go constants `KeyTSEConfigured`/`KeyTSEFailingSince` renamed to match;
   `KeyOverride*` kept their (already neutral) identifier names and only changed
   value. `fiscal.system_of_record` untouched — it was never TSE-named.
2. **Migration `009_rename_fiscal_signing_settings_keys.sql`** — renames each
   `settings` row in place so an already-configured till keeps its posture across
   the upgrade with no operator action.
3. **API route** — `POST /api/fiscal/tse-override` → `POST /api/fiscal/signing-override`,
   handler `createTSEOverride` → `createSigningOverride`. A rename, not a redirect:
   the only callers are this product's own settings page and its test fixtures.
4. **Credential store** — `TSECredentialStore` → `SigningDeviceCredentialStore`,
   on-disk `fiscal/tse_operational_credential.json` → `fiscal/signing_device_credential.json`,
   with a self-healing rename in the production constructor so a till provisioned
   before the rename does not have to re-provision (the cloud endpoint is single-use
   and would 410 a second fetch).

## Blast radius vs. the ADR's file list

The ADR named ~9 files; the diff touches 25 + 2 new. Every file beyond the ADR's
list was read individually against `git diff`, and all of them are justified:

- **A genuine call site** (must change or the build breaks): `internal/cloudsync/cloudsync_test.go`
  calls `fiscal.NewSigningDeviceCredentialStore()`.
- **Doc-comment references to a renamed symbol** (would have gone false otherwise):
  `internal/data/pos_repo.go`, `internal/pages/cloudsync_wire.go`,
  `internal/pages/common/state.go`, `internal/pages/elevation.go`,
  `internal/pages/fiscal_sign_hook.go`, `internal/pages/fiscal_signer_banner.go`,
  `internal/pages/inventory_api.go`, `internal/pages/setup_page.go`,
  `internal/pages/pos_api.go`. Comment-only, verified line by line.
- **Test files** exercising the renamed constants/route: the three gate tests the
  ADR named plus `fiscal_chip_test.go`, `fiscal_sign_hook_test.go`,
  `fiscal_signer_banner_test.go`, `setup_tse_test.go`,
  `inventory_return_fiscal_gate_test.go`, `refund_fiscal_gate_test.go`,
  `shifts_cash_adjustment_fiscal_gate_test.go`.
- **`scripts/ci/deadcode-baseline.txt`**: exactly three pre-existing entries
  renamed in place (`NewTSECredentialStoreAt`, `TSECredentialStore.Exists`,
  `TSECredentialStore.Path` → their new names). No new dead-code exemption was
  added — checked line by line, since a smuggled-in exemption here is precisely
  what this file could hide.

**Verdict on scope**: the diff matches ADR-0081's *intent* (pure rename, zero
behaviour change). It touched more files than the ADR anticipated because a real
rename has more call sites than a design document enumerates; nothing here is
unnecessary widening, and nothing was reverted as scope creep.

## Is it really a pure rename?

Every changed non-comment line containing a string literal was extracted from the
diff and read (`git diff -U0 -- '*.go' | grep …`). All of them are mechanical
renames. Specifically confirmed **unchanged**, which is what ADR-0081's
"audit-log content byte-for-byte identical" promise actually rests on:

- audit action `tse_configured_changed` in `settings_page.go` — correctly left
  alone next to a renamed `case fiscal.KeySigningDeviceConfigured:`. Renaming it
  would have broken continuity with historical audit rows.
- permission action `fiscal_tse_override` passed to `canPerform` — correctly left
  alone; it is seeded in `role_permissions`, so renaming it would silently revoke
  the override capability.
- `finishTSEProvisioning`'s audit message `"TSE operational credential stored"`.

Operator-visible text: **none changed**. `guard-i18n` passes with 1456 template
keys resolving and all locales matching `en.json` — no locale key was added,
removed or renamed, so no lang-pack check was needed. The only runtime strings
that changed are two `http.Error` bodies on the generic settings-upsert endpoint,
both of which *had* to change because they quote the route and key names that
genuinely moved; both were already hardcoded English before this diff, so no new
i18n obligation is created.

## Findings

### 1. BLOCKING — migration 009 bricks a joined till on boot in a multi-till shop. Fixed.

The ADR reasons that the rename is "safe under `settings.key`'s PRIMARY KEY: at
most one row matches; a fresh install has neither key." That is correct for a
single till and wrong for a multi-till shop.

None of these five keys is in `data.PerTillSettingPrefixes`
(`{"sync.", "printer.", "display.", "reports.eod_", "fiscal.pending_sign_retries"}`),
so they are shop-wide: a primary dumps them into the admin bundle and every joined
till upserts them. And `sync_admin_repo.go` **deliberately never prunes `settings`
rows** — its Phase-1 skip exists precisely so "a key a newer replica writes that
the primary doesn't know must not be wiped on every pull (version skew)". The same
file names "two tills mid-rollout on different migration versions" as an
anticipated state, and there is no version gate on sync.

So the ordinary staggered upgrade of a multi-till shop reaches a state the bare
`UPDATE` dies on:

1. Primary upgrades; its row is renamed to `fiscal.signing_device_*`.
2. The still-old replica syncs, upserts the **new** key, and keeps its own **old**
   key (never pruned) — the replica now holds both.
3. The replica upgrades. `UPDATE settings SET key = <new> WHERE key = <old>` hits
   `UNIQUE constraint failed: settings.key`.

Because `execMigrationStatements` runs each migration inside a transaction, this is
not partial corruption — it is worse: the whole migration rolls back, the ledger
row is never written, and **every subsequent boot fails identically**. The till
cannot sell at all until someone edits the database by hand.

Reproduced empirically before fixing (a throwaway probe seeding both keys made
`Open()` return
`run migrations: exec migration 9 (…): constraint failed: UNIQUE constraint failed: settings.key (1555)`).

**Fix applied**: each `UPDATE` is now preceded by a guarded
`DELETE FROM settings WHERE key = '<old>' AND EXISTS (SELECT 1 FROM settings s WHERE s.key = '<new>')`.
When both rows exist the **new**-named row wins — it is the value that arrived from
the primary, which is the source of truth for a shop-wide setting, whereas the
old-named row is this till's own pre-upgrade copy of the same setting (in practice
identical; this only decides the tie). Both statements stay idempotent and no-op on
a fresh install and on a re-run. The migration file carries the full reasoning
inline so the next person renaming a replicated settings key doesn't rediscover it
the hard way.

**Regression test added**: `TestFiscalSigningKeysRename_MultiTillSkewDoesNotBlockBoot`
seeds both names for all five keys, asserts `Open()` succeeds, asserts the synced
value wins, asserts no `fiscal.tse_*` row survives, and asserts the gate still
reaches `Allowed`. Verified to fail against the pre-fix migration with the exact
`UNIQUE constraint failed` boot error, and pass after.

This is the finding that justifies the different-model review: it is invisible from
inside the single-till mental model the ADR itself was written in.

### 2. BLOCKING (CI) — `guard-docs-shots` was red. Fixed.

`guard-docs-shots.sh` is a hard gate in `ci.yml`, and it hashes **whole files** for
any `internal/pages/**.go` that registers a screenshotted route. `settings_page.go`
and `setup_page.go` both do, so even this diff's comment-only edits to them
invalidate the recorded `surface_sha256`. Confirmed the guard is green on clean
`main` (in an isolated worktree) and red with the diff applied, so this was
introduced here, not inherited.

Fixed by running `make docs-shots` and committing the result. The outcome
independently corroborates the "no UI change" claim: of 100 regenerated
screenshots, **zero** changed content — the only change is `manifest.json`'s
`surface_sha256`. (One 2-byte PNG delta on the unrelated `fa/multitill` shot was
rendering nondeterminism and was reverted rather than committed as noise.) Also
verified there is no fiscal seeding in the docs-shot fixtures or `seeddata`, so the
fiscal chip is never exercised in a configured state by these screenshots.

### 3. Accepted — `SigningDeviceCredentialStore.Exists` is now a stale baseline entry.

The new self-heal calls `s.Exists()` from the production constructor, so `Exists`
is no longer unreachable, yet it remains in `deadcode-baseline.txt`. This is not a
CI failure: `guard-deadcode-baseline.sh` fails only on entries that are **new**;
one that disappears is reported as "burned down" with exit 0, and the script says
refreshing is "not required". Removing the line by hand would be the riskier move
here — the guard cannot run in this sandbox (missing gtk+-3.0/webkit2gtk-4.1,
confirmed failing identically on unmodified `main`), so a hand-edit could not be
verified and would hard-fail CI if the symbol is still unreachable from the
analysis roots. Left as-is deliberately.

### 4. Accepted — residual TSE vocabulary outside ADR-0081's scope. Filed as ut-docs#1738.

`fiscal.tse_provisioning_state` is still TSE-named (and is a settings key, so it
needs its own migration); so are `BlockedTSEFailing`, `applyFiscalTSEReady`,
`TSEOverrideResponse` and friends, plus the file names `tse_credential_store.go`
and `setup_tse.go`. All genuinely out of the ADR's enumerated scope — ADR-0081
named exactly four things and the diff delivered exactly those. Backlog card
ut-docs#1738 records the list, and explicitly separates it from the names that must
**not** move without a data migration (the audit action, the permission action,
`fiscal_tse_signatures`).

### 5. No finding — credential-store self-heal is correct.

Read the code rather than the description. Confirmed: the self-heal lives only in
`NewSigningDeviceCredentialStore()` and not in `…At()` (so the test seam is
unaffected); it checks `s.Exists()` first, so an existing new-path credential is
never overwritten by an older legacy one; it stats the legacy path and returns
early on error or on a directory; and a failed `os.Rename` is logged at warn and
left alone rather than panicking or failing boot — the store then honestly reports
nothing stored, which the provisioning path treats as "fetch again". No `MkdirAll`
gap is introduced (the directory necessarily exists on any till that wrote the
legacy file), and the fresh-install case creates nothing. The three tests covering
these branches (`…MigratesLegacyTSEFilename`, `…KeepsNewFileWhenLegacyAlsoPresent`,
`…FreshInstallHasNothingToMigrate`) match the code's actual behaviour.

### 6. No finding — route rename is complete.

`grep -rn "tse-override\|createTSEOverride"` over the tree returns nothing outside
`docs/code-reviews/` (historical records, correctly left alone). `guard-data-access`
passes; no route-registration guard is sensitive to this path.

### 7. No finding — migration hygiene.

Version 009 is genuinely free (`internal/db/migrations/` tops out at 008);
`guard-migration-version-collision.sh` passes; the loader's `checkNoDuplicateVersions`
accepts it. The SQL is idempotent, a no-op on a fresh install, and correct on a real
upgrade. `fiscal.system_of_record` is correctly excluded.

### 8. No finding — secrets and demo data.

No real client or shop name. The only secret-shaped literals are obvious test
placeholders (`super-secret-operational-credential-PLOVER`, pre-existing;
`legacy-key-1`; `stale`/`current`; `admin1`).

## TDD claims, independently re-verified

Done in an isolated `git worktree` with the diff applied by patch, so the shared
checkout was never mutated mid-revert (ut-docs#386).

| Claim | Result |
|---|---|
| Reverting migration 009 fails the new migration test with "migration 9 not recorded" | **Confirmed.** Both tests fail with `migration 9 not recorded as applied on a fresh DB — has it been renumbered?` |
| Stripping the credential-store self-heal fails its test on `Load … ok=false` | **Confirmed.** `TestSigningDeviceCredentialStoreMigratesLegacyTSEFilename` fails with `Load after legacy migration: ok=false err=<nil>` |

Both restored and confirmed green afterwards.

**Extra check the claims didn't cover**: deleting a migration file only proves the
test notices the file is gone, which would be a weak test. So the migration was
also **neutered in place** (kept at version 009, body replaced with `SELECT 1;`).
The tests still fail, and fail on the *semantic* assertions —
`post-migration decision = 1, want 0` (i.e. every configured shop regressing to
`BlockedNeverConfigured`) across both DE and TR — proving the test pins the SQL's
behaviour, not merely the file's existence. The migration test also asserts
*pre*-migration that the gate does **not** already reach the wanted verdict, so it
cannot pass without 009. These are genuinely load-bearing tests.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...` — clean.
- `gofmt -l` on every changed Go file — clean.
- `go test ./internal/fiscal/... ./internal/pages/... ./internal/db/... ./internal/cloudsync/... ./internal/data/...` — all pass, after the fixes.
- Guards: `guard-data-access`, `guard-i18n`, `guard-docs-shots`,
  `guard-migration-version-collision`, `guard-help-topics` — all exit 0.
- `guard-deadcode-baseline.sh` **not run**: it needs gtk+-3.0/webkit2gtk-4.1, which
  this sandbox lacks; confirmed it fails identically on clean, unmodified `main`, so
  this is a pre-existing environment gap. Its input file was reviewed by eye instead
  (finding 3).
- Manual/help: no shop-owner-visible behaviour changed, so no `web/help/` topic
  needed updating; `guard-help-topics` confirms the structural half.
- UX checklist: not applicable — no rendered text, no locale key, no layout, no new
  modal. The zero-pixel-change screenshot regeneration in finding 2 is the evidence.

## Safe to merge

**Yes**, after the two fixes above. Finding 1 would have shipped a permanent
boot failure to every multi-till shop that upgrades its tills at different times —
the highest-severity class this product has (a till that cannot boot is a shop that
cannot sell) — and it is now covered by a regression test that reproduces the exact
failure. With that fixed, the change delivers what ADR-0081 asked for: identical
gate decisions, identical override semantics, identical audit content, only names
moved.

## Explicitly deferred

- Residual TSE vocabulary outside ADR-0081's scope → ut-docs#1738.
- Stale `deadcode-baseline.txt` entry for `SigningDeviceCredentialStore.Exists` →
  left deliberately; the guard treats it as informational, and it cannot be
  verified in this sandbox.
