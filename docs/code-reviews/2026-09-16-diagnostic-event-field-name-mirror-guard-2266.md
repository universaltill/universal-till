# Review — diagnostic-event field-name cross-repo mirror guard (ut-docs#2266)

**Date:** 2026-09-16 · **Lane:** cloud-NN · **Card:** ut-docs#2266 (complexity:medium, infrastructure)
**Build:** Sonnet (session model), no subagent fan-out (single-file, mechanical) · **Review:** fresh-context Opus subagent, read-only

## Problem

`universal-till`'s `cloudAllowedTypes` mirrors ut-cloud's `AllowedEventTypes` (the event *type* set) and is checked by `TestAllowlistedEvents_FieldTypesAreClosed` — but nothing mirrored ut-cloud's `eventFieldAllowlist` (the per-type *field-name* set added by ut-docs#2232). A field added to a till-side event struct with no matching ut-cloud allowlist entry is silently rejected — a terminal 400 per batch (`internal/cloudsync/diagnostics.go`), not retried — with no test catching it before it ships.

## Fix (`internal/diagnostics/events.go`, `events_test.go`)

- New hand-maintained mirror `cloudEventFields map[string]map[string]bool`, copied field-for-field from ut-cloud's real `eventFieldAllowlist` (`ut-cloud/internal/diagnostics/diagnostics.go`) — same discipline `cloudAllowedTypes` already uses for the type set, deliberately NOT a cross-module import or `go/ast` parse of the sibling repo (this package stays leaf-level, matching its own package doc).
- New test `TestEventFieldNamesMatchCloudAllowlist`: for every registered event, diffs its struct's json-tag field names against `cloudEventFields[eventType]` in both directions (an emitted field ut-cloud doesn't allow; an allowed-by-mirror field the struct no longer emits), plus an orphan check for a `cloudEventFields` entry whose type isn't emitted at all.

## Verification

- `go test ./internal/diagnostics/...` green; `gofmt -l` clean; `go vet` clean.
- Confirmed the guard actually catches drift, not just passes vacuously: temporarily renamed `PluginAsk`/`TableAssignment`'s `outcome` json tag → the test failed on exactly the expected two lines (extra field + stale-mirror-entry), then reverted and re-confirmed green.

## Review findings and outcome

| # | severity | finding | outcome |
|---|---|---|---|
| 1 | low (follow-up) | Guard is one-directional: pins the till against the *local* `cloudEventFields` copy, but ut-cloud's own `AllowedEventTypes`/`eventFieldAllowlist`/`eventFieldKind` have no totality check against each other — the exact "type added with no field-allowlist entry" failure mode ut-docs#2266's own Finding 1 named is still unguarded on the ut-cloud side. Inherent to this card's prescribed approach (till-side hardcoded mirror), not a defect in what was built. | filed as ut-docs#2310, not fixed here — separate repo, separate test, out of this card's scope |
| 2 | nit | `TestEventFieldNamesMatchCloudAllowlist` lacked the empty-registry `t.Fatal` guard its sibling `TestAllowlistedEvents_FieldTypesAreClosed` has | fixed: added the same guard |

Clean: `cloudEventFields` values verified byte-for-byte against ut-cloud's real `eventFieldAllowlist` (all 7 types, 30 fields) and against the struct json tags on this side; no cross-module dependency introduced; style/doc-comment convention matches `cloudAllowedTypes`/`enumValues` nearby.
