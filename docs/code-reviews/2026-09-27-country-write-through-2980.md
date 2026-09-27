# Review — store.country from an additional till (ut-docs#2980)

- **Date:** 2026-09-27
- **Lane:** lane:cloud-54
- **Branch:** `fix/2980-country-write-through`
- **Author model:** Opus 5.5. **Reviewer:** Fable (independent subagent, separate worktree).

## What shipped

Before this change, an additional till refused `store.country` in both
`POST /api/settings/upsert` and the Store card (`POST /api/settings/save`).
The main till's `POST /api/sync/settings/apply` refused it with
`not_supported_via_sync`.

- **Main till** (`sync_settings.go`): accepts `store.country` and runs the
  same country-change invariant as the local handlers
  (`fiscal_country_change.go`), using its own state:
  - While the country being left has a confirmed signing device, the
    **actor** needs `fiscal_tse_override` in the main till's role table.
    A PIN approver never grants it, the same as the upsert handler, which
    checks the session user.
  - `clearFiscalStateForCountryChange` runs before the write and is
    audited on the old country's row.
  - ut-docs#1027's locale re-derive (`derivedLocaleForCountry`) is skipped
    when `store.locale_confirmed` is set or the batch already names
    `store.locale`. Otherwise the derived locale is written in the same
    `SetMany` and returned in the answer, so the additional till mirrors
    the country and locale together.
  - The posture reset is **not** in the answer.
  - ut-docs#1068: the main till queues the new country's base plugins.
  - The country value is trimmed.
- **Additional till** (`settings_page.go`): both handlers send the country
  through the main till. They no longer reset this till's own fiscal
  state, which catches up at the next pull, and they no longer queue base
  plugins. The local `requireFiscalAuthorityForCountryChange` still runs
  as a second check.
- **Version skew:** if the main till is older, it still answers
  `not_supported_via_sync`. The replica then shows "Change this setting on
  the main till." and writes nothing.
- **Manual:** the last sentence of `web/help/{en,de,fa,ar,tr}/multitill.md`
  step 13 now says the country can be changed on a joined till. The
  `manifest.json` topic and surface hashes were refreshed without
  recapturing screenshots, because no rendered pixel changed: the Go change
  is API-only and the help change is prose.
- **Docs:** ut-docs `architecture/lan-sync.md`.

## Tests (TDD)

New tests were written first and failed on `main` for the right reason: a
400 `not_supported_via_sync` from the main till, or a 409 "Change this
setting on the main till." from the replica.

- `TestSyncSettingsApply_CountryChangeResetsPostureAndDerivesLocale`,
  `…NeedsActorFiscalAuthorityWhileConfigured` (manager, and cashier with an
  admin approver → 403, nothing written), `…ByManagerWithoutConfiguredDevice`
  (confirmed locale kept, value trimmed), `…CountryWithExplicitLocaleKeepsIt`,
  `…SameCountryIsNoChange`.
- `TestSettingsWriteThrough_CountryUpsertGoesThroughMain` and
  `…CountrySaveGoesThroughMain`: the owner on the replica makes the change;
  the main till has it with the posture reset; the replica's posture is
  untouched and nothing is queued on the replica.
  `…CountryByManagerRefusedWhileConfigured`,
  `…CountryMainTillDecidesAuthority` (the replica's copy says no device;
  the main till still refuses), `…CountryOlderMainPointsAtMainTill`.
- Removed `TestSettingsWriteThrough_StoreSaveCountryPointsAtMainTill`. It
  pinned the old refusal this card replaces, and
  `…CountrySaveGoesThroughMain` now covers the same path.

## Review findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major (CI) | `manifest.json` `surface_sha256` was stale: it was hashed before the last comment edit. | **Fixed:** re-ran `update-docs-shots-surface-hash.sh` on the final tree; `guard-docs-shots.sh` passes. |
| 2 | minor | Base plugins were queued on the replica too. That writes the shop-wide `setup.pending_base_plugins` locally and installs the pack on the replica itself. | **Fixed:** the queue is gated on not following a main till in both handlers; the main till queues it. The test asserts an empty queue on the replica and was confirmed to fail without the gate. |
| 3 | minor | The replica's live locale can differ from the mirrored DB value until the next pull, when the two tills have different language packs. | **Accepted:** this errs on the safe side (no RTL without its pack), heals itself at the next pull, and can't push stale state back. |
| 4 | nit | `store.country` was stored untrimmed on the main till. | **Fixed.** |
| 5 | nit | Case-only change ("de" vs "DE"), duplicate keys in one batch, and a 500 on a failed `locale_confirmed` read. | **Accepted:** matches the local handlers, or fails closed. |

The reviewer re-verified the TDD claims: 8 of 10 new tests fail on the old
code. The other 2 are a regression pin and the old-main compatibility test.
All 10 pass on the branch.

## Verified

- `go build ./...`, `go vet`, `gofmt`, `golangci-lint` (0 issues), full
  `go test ./...` green.
- Guards: docs-shots, help-drift, help-topics, i18n, data-access,
  kiosk-engine, core-neutral, compliance-claims, page-http-error.
- Locally `guard-deadcode-baseline` flags `internal/logging` (not touched
  here) because the desktop root can't build without GTK headers, and
  `guard-shellcheck-version` can't run because shellcheck isn't installed.
  CI runs both.
- No UI surface changed, so no visual check was needed. The driven check
  is the two-till httptest pair: the real main-till handler behind an HTTP
  server with its own database, and the real replica handlers.

## Verdict

Safe to merge. Deferred: ut-docs#2998 covers the replica's cloud-directive
path for `store.country`, which still resets fiscal state locally.
