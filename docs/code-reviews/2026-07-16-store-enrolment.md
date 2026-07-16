# Code review — first-boot anonymous store enrolment (ADR-0013 increment 1a-POS)

**Date:** 2026-07-16 · **Branch:** feat/store-enrolment · **Reviewer:** self (Claude), recorded per repo rules

## What changed

New `internal/enroll` package: on startup the till loads (or generates and
persists) a stable `device_id`, registers itself anonymously with the
marketplace (`POST {endpoint}/v1/stores/register`, shipped mp-side in
ut-market-place c70bdd8), and persists the returned `store_id` /
`merchant_id` / device `token` in the settings table
(`marketplace.*` keys). It also fetches and pins the marketplace's Ed25519
release-signing key (`{base}/ui/api/signing-key`) when
`UT_MARKETPLACE_PUBLIC_KEY` is unset — the missing piece that previously
forced hand-pasting the key into `pos.env`.

Wiring:
- `main.go`: `enroll.Init` after `LoadRuntimeConfig`, before `plugins.Init`,
  so the persisted identity fills the empty `cfg.Marketplace` fields at
  startup (telemetry picks up the stable device id too).
- `internal/pages/plugin_api.go` + `plugins_store_page.go`: install/download/
  update/entitlement handlers now read identity through
  `enroll.Effective(d.Cfg)` — a per-request **copy** of the config with the
  live enrolled identity applied — so a till that enrols after boot installs
  without a restart, without racing on the shared config.
- `packaging/pos.env.example`: documents that identity fields can stay unset.

## Precedence (the design decision)

Explicit configuration always wins; enrolment only fills gaps:
- `UT_MARKETPLACE_CLIENT_ID` set → **no auto-enrolment at all** (operator
  chose a merchant identity; don't mint another org).
- `UT_MARKETPLACE_STORE_ID` explicitness is tracked via `os.Getenv` because
  the config defaults it to the store *name* — emptiness can't distinguish
  "operator chose this" from "default".
- Signing key: fetched even for explicitly-configured tills when
  `UT_MARKETPLACE_PUBLIC_KEY` is unset; persisted on first sight (pin), never
  silently replaced by a later fetch (`needKey` is false once set).

## Offline-first review

Registration runs in a background goroutine with backoff (30s → 2m → 5m →
15m → 30m repeating); startup and checkout are never blocked. A till that
never reaches the marketplace works fully offline, forever, with only
periodic quiet retries.

## Concurrency review

- Live identity lives behind a package `RWMutex`; handlers get a config
  **copy** (`Effective`) so nothing mutates shared state at request time.
- `cfg` is only mutated in `Init`, before the server starts (same pattern as
  `LoadRuntimeConfig`).
- `storeIDExplicit` is written once in `Init` pre-server, read-only after.
- Tests run with `-race`.

## Security review

- The device token is stored in the settings table — same posture as the LAN
  sync bearer (ADR-0011); not logged.
- Signing key is TOFU-over-TLS at enrolment time, equivalent in trust to the
  documented `/ui/api/signing-key` fetch, and pinned in settings thereafter.
  The key is validated (ed25519, 64 hex chars, 32 bytes) before adoption.
- Register response is bounded-read on error paths; input validated
  (store_id + token required, merchant_id falls back to store_id).

## Testing

- Unit (`internal/enroll`, `-race`): fresh-till enrolment persists all keys
  and reaches `Effective` without touching shared cfg; already-enrolled skips
  registration; explicit config skips enrolment but adopts the device id;
  keyless-but-explicit till still fetches the signing key; retry loop
  recovers after failures; invalid signing key rejected and not persisted.
- **Live E2E against production** (marketplace.universaltill.com): fresh till
  with only the shipped endpoint config auto-enrolled
  (`store-94effd70-…`, generated `till-…` device id), pinned the prod signing
  key (`581306fb…`, matches a direct curl), then **downloaded and installed
  the FAQ plugin v0.2.2 through the Plugin Store with zero manual identity
  config** — signature verification passed with the auto-fetched key.
  Restart re-used the persisted identity (no second registration).
- `go build ./...`, full `go test ./...`, `guard-data-access.sh`,
  `guard-i18n.sh` all green. No template/nav changes (Playwright e2e surface
  untouched).

## Known limits / follow-ups

- Marketplace-side dedup-by-device idempotency is still a follow-up
  (ut-market-place); a wiped-DB till re-enrols as a new store. Harmless but
  creates orphan anonymous orgs.
- 1b: listing `access` (public|registered) field + enforcement at
  download-token issuance.
- Increment 2: claim the anonymous store via id.universaltill.com; no UI
  surface for the enrolled identity yet (settings page chip would help
  support conversations).
