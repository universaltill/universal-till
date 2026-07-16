# Code review — deregister (reset enrolment) for testing

Date: 2026-07-16
Branch: `feat/enrol-reset`

## Why
First boot auto-enrols in the background, so the till is registered before an
operator can watch/test the manual "Register now" flow (Farshid installed 0.2.1
and "couldn't test the register process — the device auto-registered").

## Changes
- `enroll.Reset(ctx, kv)` clears the stored marketplace identity (store_id,
  merchant_id, token, enrolled_at, device_registered) and the in-memory state,
  so `CurrentStatus().Registered` flips to false. Keeps the stable device id and
  the pinned signing key. Refuses when identity came from the environment.
- `POST /api/enrol/reset` (manager-only, audited `enrolment_reset`, HX-Refresh).
- Settings "Till registration" card: a `<details>` "Testing: deregister this
  till" with an hx-confirm button (manager only). After reset the card shows the
  unregistered state + "Register now", so the manual flow is testable. A restart
  auto-enrols again (production behaviour unchanged).
- i18n `settings.enrol.reset_*` in all four locales.

## Risk
Manager-gated, audited, reversible (press Register now, or restart). Does not
touch sales/catalog. No schema change.

## Checks
`go build ./...`, `go test ./...`, i18n guard (607 keys), data-access guard — green.
