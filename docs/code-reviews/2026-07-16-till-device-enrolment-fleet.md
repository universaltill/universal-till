# Code review — till device enrolment under a store + fleet view

Date: 2026-07-16
Branch: `feat/device-enrolment-fleet`

## Why
Pairs with the marketplace two-tier enrolment: the first/only till registers
the store **and its device**; a second till that joins the shop over LAN shares
the same store identity and registers **its own device** under it. The operator
can then see the whole fleet from Settings. (Farshid: "if it is a single POS or
first one, it can register the store and the device.")

## Changes
- **`internal/db/replica.go`** — `ReplicaIdentity` gains `DeviceID`.
  `ApplyReplicaIdentity` (runs after the join snapshot restore) overwrites the
  inherited `marketplace.device_id` with this replica's own fresh id and clears
  `marketplace.device_registered`, so the till re-registers as a distinct
  device. The shared `store_id`/`token`/`public_key` (from the snapshot) are
  untouched → shared entitlements.
- **`internal/pages/sync_api.go`** — the join flow mints a fresh
  `till-<random>` device id into the staged identity.
- **`internal/enroll/enroll.go`**:
  - `register()` (first till) marks `marketplace.device_registered = device_id`
    (the store register already recorded device #1).
  - New `registerDevice()` posts to `/v1/stores/devices/register` with the
    shared store token when the till has a store identity but its device isn't
    registered yet (a replica). Wired into the background retry loop
    (`needDevice`), best-effort, offline-safe.
  - New `Fleet()` fetches the store's devices from the marketplace.
- **Settings "Till registration" card** — lazy-loads the fleet
  (`GET /api/enrol/devices`, hx-trigger load); handler renders the device list,
  escaping names/ids; "unavailable" on any network failure (offline-first).
  New i18n keys `settings.enrol.fleet_*` in all four locales.

## Risk
- All marketplace calls are best-effort and never block the kiosk / checkout.
- Device-id reissue only fires for replicas (identity file present) and is
  idempotent (marker keyed to the device id). The store identity is preserved.
- Fleet fetch failure degrades to a muted message; no error surfaced to the
  sale flow.

## Tests
- `internal/db/replica_test.go` — reissues device id + clears marker, keeps the
  shared store identity.
- `go test ./...`, data-access + i18n guards (603 keys) green.

## Note
Until the marketplace pod rolls the new endpoints, the fleet list shows
"unavailable" (graceful). Device registration retries in the background.
