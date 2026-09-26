# Review: shop-wide settings write through to the main till (ut-docs#2791, first slice)

- **Date:** 2026-09-26 · **Lane:** `lane:cloud-24` · **Complexity:** hard (moved up from medium: a new cross-till authorization path)
- **Built by:** Opus 5.5 (Dev subagent) · **Reviewed by:** Fable (independent subagent, separate worktree)
- **Rest of the card:** ut-docs#2948 (store card / `SaveState` handlers, shop-wide keys on other pages, read-only rendering, a guard against direct writes). Found in review: ut-docs#2950.

## What shipped

1. **`data.SettingScope(key)`** returns per-till, shop-wide or unclassified. Shop-wide keys are listed explicitly in `ShopWideSettingPrefixes`. What the admin bundle sends does not change: `PerTillSettingPrefixes` and `perTillSetting` are untouched. `internal/data/setting_scope_test.go` scans the source for settings keys (literals, resolved constants, the `SaveState` map, keys seeded by migrations). It found 125 keys and fails on any unclassified one, with anchor keys and a minimum count so it cannot pass by finding nothing.
2. **Main-till endpoint `POST /api/sync/settings/apply`** (`internal/pages/sync_settings.go`).
   - Bearer-authenticated (`syncTill`), sync-exempt, and pinned in `TestSyncPullPathsAreExempt`.
   - Refuses:
     - 409 when the main till itself follows another till;
     - per-till or unclassified keys;
     - the fiscal keys the generic editor never accepts;
     - `store.country` (ut-docs#2948);
     - more than 50 entries or oversized bodies.
   - The approver (when PIN-elevated) or else the actor needs `settings` in the **main till's own** role table. Clearing a fiscal override or changing a posture flag also needs `fiscal_tse_override` on the actor.
   - Writes all keys in one `SetMany` and records one audit row per key (`via: till-sync` plus the till's name). It then re-derives the main till's own cached globals (the #2790 hook) and nudges the linked tills.
3. **Additional-till helper `saveShopSettings`** (`internal/pages/settings_sync_proxy.go`).
   - Shop-wide keys go to the main till first; per-till keys save locally only after the main till accepts.
   - On a 200 the keys are mirrored locally. On any failure nothing is written locally, and the handler's side effects and audit are skipped.
   - Values are never logged.
4. **Handlers wired:** payments-default, order-no-scheme, order-type-prompt, payments-fee, auto-register, telemetry, catalog-import-barcode-default, shop-type, dismiss-restore-prompt, till-name, and the generic upsert.
5. **User-facing text:** three new message keys (en/ar/fa/tr, with the de/es pack PRs following) and step 13 in the `multitill` help topic, in all five languages.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | Upsert of `store.locale` on an additional till bumped `store.locale_generation` locally only, so the next pull reverted it and retired overrides came back. | **Fixed:** the bump travels in the same batch as the locale, and the live generation is published from it. Test `UpsertImpliedKeysOneBatch`. |
| 2 | minor | A currency change sent `store.currency` and `store.currency_confirmed` in two round trips. The second could fail after the first landed. | **Fixed:** one batch. The test fails without the fix (`main till calls = 2, want 1`). |
| 3 | minor | `store.country` on an additional till said "The main till refused this change." even though the main till was never asked. | **Fixed:** new key `settings.error.change_on_main_till`. Test `CountryPointsAtMainTill`. |
| 4 | minor | The 403 body "forbidden by the main till" was hardcoded English and is shown to the operator by the htmx error alert. | **Fixed:** localized. |
| 5 | minor | The 4 KiB value cap could refuse a legitimate long receipt or invoice text with a misleading message. | **Fixed:** raised to 16 KiB per value and 256 KiB per body. |
| 6 | nit | With `UT_AUTH=off` an additional till has no actor, so the main till refuses every shop-wide change. | Accepted: dev/demo only, same as the ADR-0115 users precedent. |
| 7 | nit | Keys classified by "shop-wide if synced today" include per-till-looking state (restore prompt, LAN discovery id, …). | Pre-existing sync behaviour. Filed **ut-docs#2950** and linked it from the code comment. |
| 8 | nit | The source scan has blind spots: keys held in a `var`, built with `Sprintf`, or written through other repo methods. | Documented in the test header. |

Reviewer judgement on `till.name`: keep it shop-wide. It is the main till's own name. An additional till's own name is `sync.till_name`, and the Settings till-name card is hidden on additional tills, so the write-through only matters for a hand-crafted POST. When that happens, it renames the main till, with the main till's permission check.

Values are not re-validated on the main till: each handler validates them on the additional till before sending, and the main till checks keys, permissions and fiscal gates. `actor_id` is trusted from the bearer holder. That is the same trust model ADR-0115 already accepted for `users/apply`, which lets a bearer holder create an admin, so this adds no new escalation.

## Verification

- TDD red, recorded by Dev and re-verified by the reviewer: with the order-type-prompt handler reverted to a plain local `Set`, three `TestSettingsWriteThrough_*` tests fail (`main till calls = 0, want 1`; unreachable returns 200 "✓ Settings saved"; forbidden returns 200). With the fix restored they pass. I re-verified finding 2's fix the same way.
- Gate on the final tree:
  - `gofmt -l` empty, `go build ./...`, `go vet ./internal/...`, full `go test ./...` with no failures, `golangci-lint` 0 issues;
  - guard-data-access, i18n, help-topics, help-drift, docs-shots, kiosk-engine, core-neutral, compliance-claims, competitor-naming, page-http-error, demo-env all pass.
- **Screenshots:** `make docs-shots` in the sandbox rewrote all 124 PNGs, which looks like Chromium build drift. Those were discarded. No rendered pixel changes on a main till, and the help change is prose only. So only `manifest.json` was refreshed: `surface_sha256` via `update-docs-shots-surface-hash.sh`, plus the four `multitill` topic hashes. Docs-Shots-Unchanged: true.
- **Not done:** no two-till browser run. The flow is covered end to end by httptest against the real main-till handler, backed by its own migrated DB. Nothing visible changes except the error text.

## Verdict

Safe to merge. Deferred: ut-docs#2948 and ut-docs#2950. Language-pack PRs in `ut-plugin-language-de`/`-es` follow the core merge in the same cycle.
