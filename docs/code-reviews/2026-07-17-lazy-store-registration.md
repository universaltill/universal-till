# Code review — lazy store registration (ADR-0015)

**Date:** 2026-07-17
**Branch:** `feat/lazy-store-registration`
**Ask (Farshid):** multi-store is paid-licence only, so a till that doesn't
use multi-store or paid cloud services shouldn't need to register a store in
the marketplace at all.

## What changed

- **ADR-0015** (docs repo) amends ADR-0013 layer 1: enrolment is no longer
  automatic at first boot — written and accepted before this code, per
  ADR-0007.
- `internal/enroll/enroll.go`
  - `Init` no longer starts a store-registration loop. Boot still: mints and
    persists the stable `device_id` (offline), background-fetches the
    marketplace **signing key** (needed to verify any plugin bundle;
    anonymous, creates no record), and — for a replica that joined a shop and
    inherited the store identity — registers the device under the shared
    store (`needDevice`, unchanged).
  - New `EnsureRegistered(ctx, cfg, kv) config.Config`: register-if-needed,
    then return the effective config. Non-fatal on failure — the caller's
    marketplace call surfaces the real error and the next attempt retries.
  - `run()` lost its `needRegister` leg (dead after the above).
- `internal/pages/plugin_api.go` (install-from-listing + update) and
  `plugins_store_page.go` (`POST /api/plugins/store/download`) call
  `EnsureRegistered` instead of `Effective` — the first plugin
  download/install is the earliest interaction that needs a store identity
  (entitlements hang off it). `InstallFromStore` (installs an
  already-downloaded bundle) stays local, no registration.
  Settings → "Register now" (`RegisterNow`) unchanged.

## Why the marketplace needs no change

`downloadsvc` already self-provisions the merchant org at download-token
issuance for free listings, and already denies `registered`/paid listings to
anonymous/unclaimed stores (`ErrRegisteredRequired`). Boot-time registration
was never load-bearing for free installs.

## Trade-offs (in the ADR)

- Fleet visibility narrows to tills that actually used the marketplace or
  registered deliberately — accepted; no more throwaway prod store orgs from
  demo/test boots (I created several myself while testing releases).
- The amber "Register this till" chip and Settings card stay — registration
  is still the gateway to claim/fleet/paid.

## Tests

- `TestInitFreshTillDoesNotRegisterStore` (replaces
  `TestInitFreshTillEnrolsAndPersists`): fresh boot fetches the signing key,
  makes **zero** register calls, persists no store identity; the first
  `EnsureRegistered` enrols exactly once and the identity reaches handlers
  without a restart; a second call is a pass-through.
- `TestEnsureRegisteredRetriesAcrossAttempts` (replaces
  `TestRunRetriesUntilMarketplaceReachable`): two failing register responses
  → still unregistered; third attempt succeeds.
- Unchanged and still green: already-enrolled boot, explicit-env config,
  keyless signing-key fetch, bad-key rejection; full suite +
  `guard-data-access` + `guard-i18n`.
