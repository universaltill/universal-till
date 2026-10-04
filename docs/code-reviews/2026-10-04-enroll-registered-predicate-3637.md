# Code review — one "is this till registered" predicate (ut-docs#3637)

**Date:** 2026-10-04 · **Author model:** Sonnet (Dev session) · **Reviewer:** Opus 5.5 (independent subagent)
**Card:** universaltill/ut-docs#3637 (easy / p3)
**Branch:** universal-till `fix/3637-registered-predicate`

## What shipped
- **The bug.** `enroll.CurrentStatus().Registered` was `explicitConfigured || (cur.StoreID != "" && cur.Token != "")`. A till with `UT_MARKETPLACE_STORE_ID` and `UT_MARKETPLACE_MERCHANT_TOKEN` pinned in the environment, but no `UT_MARKETPLACE_CLIENT_ID`, therefore read as "not registered". Settings hid "Check for a paid plan" and showed the "not registered" chip. Meanwhile `Effective(cfg)` resolved a full identity for the same till, so the sync loop ticked and a check-plan posted by hand was accepted.
- **`internal/enroll/enroll.go`:**
  - New `credentialsReady()`: `(storeIDExplicit || cur.StoreID != "") && (tokenExplicit || cur.Token != "")`. `CurrentStatus` now uses `explicitConfigured || credentialsReady()`.
  - New `HasCredentials(cfg)`: an endpoint, store id and token are all non-empty in `Effective(cfg).Marketplace`.
  - New `CredentialsComplete(m)` (added in review): the same test applied to a config the caller has already resolved.
- **Call sites.** 15 copies of the inline three-field check now use the shared predicate:
  - `HasCredentials(cfg)` at sites that only gate: `cloudsync.tick`, the Settings check-plan handler, `alerts.pushDigest` and `pullIssueReportStatuses`.
  - `CredentialsComplete(m)` at sites that go on to use `m`: `alerts.pushNotify`, `catalog_image`, `diagnostics` (x2), `issue_reports` (x2), `cloud_link`, `setup_tse` (x2), `enroll.Fleet` and `pages.checkinAfterRegistration`.
- **Tests (`internal/enroll/enroll_test.go`):** `TestCurrentStatusRegisteredForEnvPinnedStoreAndTokenNoClientID`, `TestHasCredentialsRequiresEndpoint`, and `TestCredentialsCompleteNeedsAllThree` (added in review).
- **Not touched:** no locale keys, no `web/locales`, no `web/help`, no templates. No ADR is needed: this is a single-package predicate fix, not a new mechanism.

## Findings (reviewer)
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The Dev diff kept `m := enroll.Effective(cfg).Marketplace` at 10 sites, then guarded with `HasCredentials(cfg)`. That resolves `Effective` a second time, under a second lock. A `Pair` or token rotation in between could let the guard pass while the request is built from the other copy (for example an empty bearer). The old inline check guarded the exact value it used. | **Fixed.** Added `CredentialsComplete(m config.MarketplaceConfig)`, and `HasCredentials` is now built on it. Every site that uses `m` after the guard checks `m` itself. Test added. |
| 2 | minor | The brief excluded `operator_checkin.go`'s `checkinAfterRegistration`. The reason was sound: it must test the `eff` its caller passed, not re-resolve. But that left a duplicate of the predicate in place. | **Fixed** with the same `CredentialsComplete(eff.Marketplace)`. Its tests still pass synthetic `eff` values (`go test ./internal/pages` is green). |
| 3 | nit | The `credentialsReady` doc said it was "the shared test behind CurrentStatus().Registered and HasCredentials". It is not: `HasCredentials` deliberately doesn't use it. | **Fixed.** The comment now states the real relationship and the known divergence (#4). |
| 4 | minor | After the fix the two predicates still disagree in one case: a **token-only** env pin (no env store id, no persisted store id, no Pair). `config.Load` defaults `Marketplace.StoreID` to the store **name**, so `Effective` gives `StoreID = <shop name>`. `HasCredentials` is then true and the sync loop calls the cloud with the shop name as the store id, while `CurrentStatus` says not registered. This was already true before the diff, and it is not the issue's case. | **Deferred** (follow-up suggested): decide whether the store-name fallback should ever count as a cloud store id. If not, `Effective`/`HasCredentials` should treat it as empty. |
| 5 | nit | For the issue's case, `Status.StoreID` (`displayStoreID`) stays empty: it is only set from env when a client id is pinned. Settings now shows "registered" with no store-id line (the template guards with `{{ if enrolstore }}`). This is cosmetic and was already true before the diff. | **Accepted / follow-up** (one line in `Init`). |
| 6 | nit | Two more copies of the predicate, spelled differently, remain: `enroll.ClaimCode` and `enroll.VouchForReplica` (`m.EndpointURL == "" \|\| storeID == "" \|\| token == ""` after `currentStoreAuth(m)`). They are equivalent and not buggy. | **Deferred** (they use the separately returned `storeID`/`token`, so converting them is a separate tidy-up). |

### Scope (9 extra call sites beyond the 2 the issue named)
Note: the call sites were **not buggy**. The inline check was already `Effective(cfg)`-based, exactly what `HasCredentials` computes. The only behavioural bug was `CurrentStatus`. So the extra sites are a pure de-duplication refactor, not "9 more bug fixes". It is still the right call for this card. The duplication is how the two predicates drifted apart in the first place, the change is mechanical and behaviour-preserving (verified by green packages), and splitting it out would have left the issue's root cause, many hand-copied predicates, in place.

### Behaviour change worth knowing
Widening `CurrentStatus().Registered` also changes `EnsureRegistered` and `RegisterNow`. For an env-pinned store id + token till they now **skip** `register()`. Before, the first plugin install or "Register now" on such a till minted a fresh anonymous store in the cloud. `Effective` then ignored that store, because the env pins win, so it was orphaned. Skipping it is an improvement. The base-layout "not registered" chip is also hidden for these tills, as intended.

## Verification beyond the automated tests
- **TDD re-verified by the reviewer:**
  - Reverted only `CurrentStatus` to the old formula. `TestCurrentStatusRegisteredForEnvPinnedStoreAndTokenNoClientID` failed at `enroll_test.go:373` with `CurrentStatus() = {Registered:false ViaMainTill:false StoreID: DeviceID:till-fixed}, want Registered true …`. This is the issue's bug exactly. Restored, it passes.
  - Dropped the `EndpointURL` term from `HasCredentials`. `TestHasCredentialsRequiresEndpoint` failed at `:401` with `HasCredentials = true with no marketplace endpoint configured`. Restored, it passes.
  - Rebuilt `HasCredentials` on the package flags (`EndpointURL != "" && credentialsReady()`, the Dev's first draft). Exactly 6 `internal/alerts` tests failed: `TestPushDigest`, `…_PropagatesRunningOutCountError`, `…_PropagatesPushNotifyError`, `TestPushNotify_MalformedEndpointURL`, `TestPushNotify_TransportError` and `TestStart_RunsDigestLoopBody`. This confirms that re-resolving through `Effective(cfg)` is necessary, not over-engineering. The flags are only set by `Init`, and a directly-built cfg would read as unregistered.
- **Boolean algebra re-derived** from `Init`, `Effective`, `liveToken`, `Pair` and `adoptOwnCredential`. Notation: SE/TE = store/token env-pinned, S/T = cur.StoreID/cur.Token non-empty, R = identityReplaced, E = endpoint set.
  - `Effective` StoreID is non-empty ⇔ SE ∨ S ∨ ¬R (when ¬R it falls back to cfg, which is the store name).
  - `Effective` token is non-empty ⇔ TE ∨ T. A boot copy can only be non-empty while T is empty after a `Pair`, which sets R.
  - So `HasCredentials` ⇔ E ∧ (SE ∨ S ∨ ¬R) ∧ (TE ∨ T), and `credentialsReady` ⇔ (SE ∨ S) ∧ (TE ∨ T).
  - These agree whenever E holds, except for ¬SE ∧ ¬S ∧ ¬R ∧ TE (finding #4). T without S is not reachable outside `Pair`'s transient window, and there R makes `Effective`'s store empty, so both say false.
  - A refused `Pair` (R, cleared token/store, no pins) gives false for both. A client id alone gives registered, which is the intentional, pre-existing "explicit config counts" rule.
- **Missed-site grep:**
  - `EndpointURL == "" ||.*StoreID == ""` / `StoreID == "" ||.*MerchantToken == ""`: no hits left after the review fix. `operator_checkin.go` now uses `CredentialsComplete`.
  - The broader `storeID == "" || token == ""` form: only #6's two sites, plus `registerDeviceID`'s argument guard.
- **Recurring bug classes:** no file writes and no paths in this diff (pure logic), so no `MkdirAll` or `paths.Data` concerns. No stale `eff`/`m` variables: every remaining `m` is used after its guard.
- **Toolchain:**
  - `go build ./...`: OK.
  - `go vet` on enroll/cloudsync/pages/alerts: OK.
  - `gofmt -l` on the changed packages: no output.
  - `go test -count=1` on enroll (1.5s), cloudsync (29.3s), pages (199.5s) and alerts (6.5s): all `ok`, after the review fixes.
- **Not run:**
  - `golangci-lint`: the container's binary is built with go1.25 and the module targets go1.27.1, so it refuses to load.
  - `-race`: known sqlite-driver slowdown in this sandbox.
  - A live till against the cloud: the predicate is fully covered at unit level.

## Deferred (suggested follow-ups, not filed by the reviewer)
- #4: whether the store-name fallback for `Marketplace.StoreID` should ever count as a cloud store id.
- #5: set `displayStoreID` from an env-pinned store id even when no client id is pinned.
- #6: fold `ClaimCode` and `VouchForReplica` onto `CredentialsComplete`.

## Verdict
**Safe to merge.** It fixes the issue's case with a test that demonstrably fails without the fix. Call-site behaviour is unchanged apart from the guard now covering the exact value used (#1). Every changed package is green.
