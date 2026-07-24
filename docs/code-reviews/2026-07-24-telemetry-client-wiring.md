# 2026-07-24 — Wire TelemetryClient to the real marketplace contract

## Context
Spec-audit gap (`ut-docs/QUEUE.md`): `internal/plugins.TelemetryClient` had
zero call sites anywhere in the codebase (confirmed by grep before starting)
— never instantiated, so plugin telemetry never sent, matching the
scheduler's telemetry job being a literal `"(stub)"` log line
(`internal/server/server.go`) with a `// TODO: Implement telemetry
reporting in T024`.

A closer look found the client wasn't just unwired — its wire format was
wrong on its own terms. It POSTed a `TelemetryEvent`/`TelemetryBatch` JSON
shape to `{marketplaceURL}/v1/telemetry`, a path that never existed on the
`ut-cloud` server in any form (old or new). Three incompatible telemetry
models existed across the two repos before this change: the till's invented
event-batch shape, `ut-cloud`'s old aspirational internal Go structs
(crash/memory/CPU fields that were never in any proto message), and the
real `cloud.v1.TelemetryService.ReportPluginStatus` proto contract. None of
them agreed with each other.

## Design
Companion change to `ut-cloud`'s `2026-07-24-device-telemetry.md` (that repo
also fixed `ReportPluginStatus` being unreachable — no REST route, and raw
gRPC isn't exposed by the cluster). Rewrote `TelemetryClient` to speak the
real contract:

- Dropped the event-queue/batch-interval model and the unused
  `TrackInstall`/`TrackUpdate`/`TrackBrowse`/`TrackStatusUpdate`/`Start`/
  `Stop` methods — all had zero callers, and none of them (browse events,
  discrete install/update events) have any representation in the real proto,
  which only carries a current-state snapshot (`plugin_id`,
  `installed_version`, `status`, `source`, `device_id`).
- New `ReportNow(ctx)`: queries `data.PluginRepo.ListInstalledPlugins`
  (already filters `is_active=1`) and POSTs one full snapshot per call to
  `{marketplaceURL}/v1/telemetry/report`, JSON field names matching the
  proto exactly (`plugin_id`, `installed_version`, `status`, `source`,
  `device_id`, `statuses`, `merchant_id`, `store_id`).
- `internal/server/server.go`: `NewBackgroundJobs` now constructs the client
  from `cfg.Marketplace` (`EndpointURL`, `ClientID` doubles as merchant_id
  per this repo's own README, `StoreID`) and
  `marketplace.DeviceIDFromConfig` (existing shared helper — didn't
  reinvent the hostname-fallback logic already used elsewhere). The
  scheduler's telemetry tick calls `ReportNow` instead of logging a stub.
- Best-effort send: a failed request is logged and dropped, not
  retried/queued — the next tick sends a fresh full snapshot anyway, so
  queuing would only ever re-send stale data.

## Independent review
Same review pass as the `ut-cloud` side (opus model, reviewed both repos
together for cross-repo correctness — see that repo's review doc for the
full finding list). Findings that landed changes in this repo:

- **HIGH — telemetry could never actually turn on.** The client correctly
  gates sending on `marketplace.telemetry_opt_in`, but nothing anywhere in
  the codebase ever wrote that key — no settings UI, no default, no config
  path. The reviewer grepped the whole repo to confirm. Fixed: a
  manager-gated toggle added to the existing Settings page
  (`web/ui/pages/settings.html`, mirroring the existing printer/idle-lock
  checkbox pattern exactly), `POST /api/settings/telemetry` handler in
  `internal/pages/settings_page.go`, new `settings.telemetry.*` i18n keys
  added to all 4 core locales (en/ar/fa/tr — `bash scripts/ci/guard-i18n.sh`
  green). Off by default, consistent with "opt-in" being the actual product
  stance (the deleted old client code already said as much).
- **MEDIUM — empty `merchant_id` guaranteed rejection.** `cfg.Marketplace
  .ClientID` (used as merchant_id) is populated from persisted enrolment
  identity by `internal/enroll.Init` — but that's a **lazy background**
  process (ADR-0013); a till reporting before it completes would send an
  empty merchant_id, which the server now hard-rejects (see the `ut-cloud`
  review — that rejection is intentional there, not a bug to route around).
  Fixed here instead: `ReportNow` skips quietly when merchant_id is empty,
  same as the existing `marketplaceURL == ""` early-return. Traced the
  actual call order (`main.go`: `enroll.Init` before `server.Start`, `cfg`
  passed by pointer throughout) to confirm this is a real-but-narrow
  transient window, not an all-devices-broken bug.

Confirmed correct by the reviewer (not just my own claim): the JSON wire
shape matches `cloud.pb.go`'s proto field names exactly, and grpc-gateway's
protojson accepts snake_case on unmarshal — the most important cross-repo
correctness question for this change, verified independently rather than
trusted.

## Verification
`go build ./...`, `go vet ./...` clean. `go test ./internal/plugins/...
./internal/server/...` — new tests: opt-out skip, not-yet-enrolled skip,
active-plugin-only reporting (a disabled plugin in the fixture is correctly
excluded), no-installed-plugins no-op, server-error surfacing.
`bash scripts/ci/guard-i18n.sh` green (687 keys, all locales match
en.json).
