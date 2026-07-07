# Code Review — wire the UI install path to the live marketplace

Date: 2026-07-07
Scope: `internal/pages/plugin_api.go` — `handleInstallFromMarketplace`
(`POST /api/plugins/install-from-marketplace`).

## Problem
The endpoint returned `not_found`. It looked the plugin up in a **cached catalog
snapshot** (empty / never refreshed) and matched on `listing_id` + `device_arch`,
but the live marketplace catalog summary returns the UUID as `id` and omits
`device_arch`/`trust_tier`. Worse, a fresh fetch would fail to decode entirely —
the live summary has `"vendor":"unassigned"` (string) while the client model's
`PluginSummary.Vendor` is a struct. So the one-click install could never succeed.

## Fix
Install **directly from the listing ID**, dropping the fragile catalog parse:
- The download-token endpoint resolves the approved release for a listing with the
  version omitted (verified live: returns bundle_url + checksum + signature).
- The installer already verifies the **Ed25519 signature** and manifest
  compatibility (`device_arch`, `min_pos_version`) — the signed manifest is the
  authoritative compatibility source, so the catalog pre-check was redundant.
- The handler now builds the install request from `req.ListingID`, `systemArch`,
  and `merchant/store/device` config; `Version`/`TrustTier` are left to the
  marketplace/manifest. Status records key off `req.ListingID` and the install
  `result` (name/version).

Removed: the `CatalogRepo == nil` / `Get()` / snapshot loop / arch pre-check.
`CatalogRepo` and `PluginSummary` are still used by other handlers (browse,
update), so no imports were dropped.

## Verification (performed live against the marketplace)
- Install: `POST …/install-from-marketplace {"listing_id":"1295b44c…"}` → **200**,
  `success:true`, `plugin_id:com.universaltill.ut-faq`. DB row `installed`; FAQ
  entry `page "Help / FAQ" /plugin/faq (help_support)` registered.
- **Idempotent** re-install → 200.
- **Bad listing id** → 400 (graceful classify, not a 500).
- `go build ./...`, `gofmt`, and full `go test ./...` green.

## Findings / follow-ups
- Requires `UT_MARKETPLACE_DEVICE_ID` (or hostname fallback) — otherwise the token
  request errors "device_id is required". Documented in the install-testing notes.
- The **browse** endpoint still forwards raw catalog JSON; aligning
  `PluginSummary` with the live summary schema (vendor string vs struct, camelCase
  fields) is a separate, larger cleanup — not needed for install to work.
- Entitlement is still hand-granted (merchant-1/store-1); a self-serve "acquire"
  flow remains open.

## Disposition
Approved. The one-click POS install now works end-to-end against the live
marketplace, resting on the signed manifest as the source of truth.
