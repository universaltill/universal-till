# Review: fewer background cloud calls from an unpaid till (ut-docs#3625)

- **Card:** ut-docs#3625. Branch `fix/3625-unpaid-till-background-calls`.
- **Design:** ADR-0148 "Audit (2026-10-06)". The product owner decided items
  1–3 on the card on 2026-10-07 and picked the recommended default for each.
  The ADR records the decisions in the same cycle (ut-docs PR).
- **Build:** Opus 5.5, `lane:cloud-41`, `complexity:medium`.
- **Review:** Fable 5.1, independent of the author, one round.

## What shipped

1. **No catalog-sync ticker (audit item 1).** `internal/server.BackgroundJobs`
   has lost its catalog job. Removed with it: `syncCatalog`, `catalogKey`,
   `deviceArchOf`, the `catalogRepo`, `catalogSyncInterval` and
   `retryBaseDelay` fields, and the tests for them.
   - The catalog now refreshes only when an operator opens a page that shows
     it. That page calls `CatalogRepository.GetOrFetch`, which serves a stale
     cache and refreshes it once in the background.
   - `NewBackgroundJobs` no longer takes the catalog repo.
   - `server.Start` still starts the telemetry and revocation jobs only when
     a marketplace is configured.
2. **Status-light probe TTL 10 s → 60 s (audit item 2).**
   - `netreach.DefaultTTL` is now 60 s. The status bar still polls every
     10 s, but each poll is answered from cache.
   - Result: at most one `/healthz` probe a minute (about 60 an hour, down
     from about 360).
   - The help text in `sell.md` (en, de, ar, fa, tr) now says the light can
     take up to a minute to change.
3. **Signing key fetched lazily (audit item 3).**
   - `enroll.Init` no longer fetches the key at boot. Its `run` loop now
     only registers a main till's own device under a store that till
     already has.
   - The new `enroll.EnsureSigningKey` fetches the key, persists it, and
     returns the effective config. It shares the `attemptSem` slot and gives
     up after 5 s (`lazyKeyTimeout`).
   - It is called before every path that verifies a bundle:
     - marketplace install;
     - update (manual, and the language-pack auto-apply);
     - store install-from-download;
     - cloud-pushed, replica and setup-wizard installs, all through
       `cloudInstallPluginVersion`;
     - file import.
   - File import used to read the startup `d.Cfg` key. It now reads the
     effective key, so a key fetched after boot also verifies imports.

## Findings (Fable review)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| — | security check | Does the lazy key let any bundle install unverified? | **No.** `Install` and `InstallFromStore` fail closed without a key. Every verifying `NewMarketplaceInstaller` caller goes through `EnsureSigningKey`. The untouched `storeInstaller` callers (page render, delete download) verify nothing. |
| — | concurrency | Deadlock in the `attemptSem` slot? | None. No caller of `EnsureSigningKey` already holds the slot. Acquire and release are paired. `-race` is clean. |
| 1 | should-fix | No handler-level test pinned the lazy fetch. Dropping the call from an install path would have gone unnoticed. | **Fixed.** Added `TestStoreAPI_InstallFetchesSigningKeyLazily`: a keyless till installs, the key is fetched once, and a second install reuses it. With the call removed, the test fails with `400 marketplace public key not configured`. |
| 2 | should-fix | Stale comment in `internal/app/app.go` about the "catalog sync" job. | **Fixed.** |
| 3 | should-fix | `docs/data-model.md` still described the 15-minute catalog sync. | **Fixed.** |
| 4 | should-fix | On an offline till, a file import could block for up to 15 s (plus the time queued for the slot). The download handler also fetched a key it does not need. | **Fixed.** `EnsureSigningKey` is now bounded at 5 s. `TestEnsureSigningKeyBoundedByLazyTimeout` takes 15 s without the bound and fails. The download handler no longer fetches the key (`DownloadToStore` verifies nothing; install does). |
| 5–7 | nit | Package doc, `EnsureSigningKey` doc ("never from a background loop" was inaccurate, since the language-pack auto-apply reaches it as part of an install) and comment wrapping. | **Fixed.** |
| 8 | nit | `specs/009-cloud-marketplace/quickstart.md` still mentions the 15-minute sync. | **Accepted.** It is a historical spec. |

## Verified

- **The reviewer re-ran the TDD claim** with `enroll.go` reverted and the
  new tests kept. `TestInitFreshTillDoesNotRegisterStore` and
  `TestInitKeylessExplicitTillFetchesSigningKeyOnlyWhenAsked` both fail with
  `signing key fetched 1 times at boot; it must be lazy`.
- **`TestTTLRespected`** now polls every 10 s up to 59 s with no probe, then
  probes at 61 s.
- **Background-loop tests.** These three now drive `run()` through a seeded
  store whose device is not yet registered:
  - `TestInit_BackgroundLoopJoinsOnCancel`
  - `TestBackgroundLoopStillAcquiresAttemptSlotOnceFree`
  - `TestBackgroundLoopExitsPromptlyWhenCancelledWhileQueuedOnAttemptSlot`

  The blocking handler drains the POST body first: `net/http` only notices
  a client disconnect after the body has been read.
- **Gate:**
  - `go build ./...` and `go vet` pass.
  - `go test ./...` passes. `internal/plugins` hit the 10-minute default
    timeout under the full parallel run. It is untouched here, and passes
    alone in 531 s.
  - `go test -race ./internal/enroll/` passes.
  - Guards pass: data-access, netaccess, i18n, compliance-claims,
    competitor-naming, core-neutral, help-topics, help-drift, kiosk-engine,
    no-showmodal, pipefail-grep-q, page-http-error.
- **Not run locally:** `golangci-lint`. The installed build is go1.25 and
  refuses the go1.27.1 target, so CI runs it. A manual dead-code check found
  nothing dangling.
- **No visual surface changed.** Only help prose changed, so there are no
  screenshots.

**Verdict:** safe to merge.
