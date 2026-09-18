# Code review — plugin revocation feed decode mismatch (ut-docs#2380)

- **Date:** 2026-09-18
- **Ticket:** ut-docs#2380 (`complexity:hard`, `security`, `p1`)
- **Branch:** `fix/2380-revocation-wire-format`
- **Reviewer:** independent pass, Opus subagent (per this card's
  `complexity:hard` routing, `MODEL-ROUTING.md` — Opus, deliberately not
  Fable), isolated in its own git worktree (cleaned up on completion).
- **Verdict: SAFE TO MERGE.** No blocking findings; two should-fix items
  folded in, two pre-existing-but-adjacent gaps filed as separate
  Backlog follow-ups (not this ticket's scope).

## The bug

`internal/plugins.RevocationChecker.SyncRevocations` polls ut-cloud's
`GET /v1/revocations` on a 30-minute ticker and is meant to disable any
plugin ut-cloud has revoked (ADR-0006, plugin trust chain). It decoded
the response into a `RevocationEntry` struct tagged with till-side
snake_case keys (`plugin_id`, `developer_id`, `version`, `reason`,
`revoked_at`). ut-cloud's real HTTP gateway
(`ut-cloud/internal/httpapi/router/router.go:699`, a plain
`runtime.NewServeMux()` with **no** `UseProtoNames`/marshaler option set
anywhere in that repo — confirmed by grep, independently re-confirmed by
the reviewer) serves grpc-gateway's default protojson field names —
camelCase, driven by the proto message's `json_name`, never snake_case.
ut-cloud's actual `Revocation` message
(`ut-cloud/pkg/contracts/cloud/v1/cloud.pb.go:850`) only has `plugin_id`
(wire name `pluginId`), `version`, `action`, `reason` — no `developer_id`
or `revoked_at` field exists on the wire at all.

**Consequence: every entry's `PluginID` always decoded as `""`**, so
`GetPlugin(ctx, "", version)` never found a match and a revoked/malicious
plugin was never actually disabled on any till syncing this feed —
enforcement was a complete no-op. This was invisible because of a
**second** bug: `processRevocation`'s "not found" no-op path returned a
`nil` error, which `SyncRevocations` counted as a successful disable
alongside real ones, so the existing `"disabled %d revoked plugins"` log
line (`internal/server/server.go:123`) kept reporting a plausible-looking
number the whole time.

## What shipped

- `internal/plugins/revocation.go`: `RevocationEntry`'s JSON tags now
  match the real wire format (`pluginId`, `version`, `action`, `reason`).
  `DeveloperID`/`RevokedAt` dropped (never existed on the wire, always
  silently zero-valued). New `Action` field (`"disable"|"delete"`)
  decoded for completeness but not yet acted on differently — every
  entry is still treated as a disable, matching pre-fix behavior;
  differentiating a real uninstall (`internal/plugins.UninstallPlugin`)
  for `"delete"` is a deliberate, explicit follow-up, not folded into
  this security fix. `RevocationFeed.UpdatedAt` (never on the wire
  either) replaced with `LatestVersion string` (ut-cloud's
  `latest_version` is an `int64`, which protojson encodes as a JSON
  *string* — kept as a string, not mis-decoded as a number; nothing
  reads it yet).
- `processRevocation` now returns `(bool, error)` instead of just
  `error` — the bool distinguishes a genuine disable from a legitimate
  no-op (not installed / already disabled), so `SyncRevocations`'s
  revoked-count only increments on a real disable. This is what makes
  the existing "disabled N" log line trustworthy again, not just the
  decode fix on its own.
- Audit-log entry: dropped the always-empty `developer_id`/`revoked_at`
  keys; added the feed's requested action under `requested_action` (not
  a bare `action`, which would read as duplicating the audit row's own
  `action` column, already `disable_revoked` — an independent-review
  finding, folded in).
- `internal/plugins/revocation_test.go`: the mock feed server used to
  build its HTTP response by `json.Marshal`-ing the SAME `RevocationEntry`
  Go struct the decoder reads — a perfect round-trip through the till's
  own (wrong) tags that passed even though production was completely
  broken. Rewritten to write ut-cloud's actual wire-format JSON as a raw
  string literal, so it exercises the real contract. New
  `TestRevocationEntryDecodesUtCloudWireFormat`: a narrow, HTTP/DB-free
  unit test decoding a literal wire-format blob directly.

No `ut-cloud` changes — the ticket's own "recommended fix" chose the
smaller, safer, single-repo fix (change the till's tags) over changing
ut-cloud's marshaler options, which would affect every grpc-gateway
response repo-wide and needs its own audit.

## What the independent review found

**Wire-format claim independently re-verified from source, not trusted**:
confirmed `router.go:699`'s bare `runtime.NewServeMux()` and the zero
`UseProtoNames`/`WithMarshalerOption`/`JSONPb` hits repo-wide in
ut-cloud; confirmed `cloud.pb.go:850-858`'s exact `Revocation` struct
tags and `:798-804`'s `GetRevocationsResponse.latest_version int64`.

Ran the full gate live: `gofmt -l .`, `go build ./...`, `go vet ./...`,
`go test ./internal/plugins/... ./internal/server/...` and the full
`go test ./...`, `golangci-lint run ./internal/plugins/...` (0 issues),
`guard-data-access.sh`, `guard-i18n.sh` — all green.

**TDD claim independently re-verified, done for real, two variants**:
reverted the struct tags alone (kept the new counting logic) — red,
`processed = 0, want 1` (both entries decoded, neither matched, since
`PluginID == ""`). Reverted both the tags AND the counting logic to the
full pre-fix shape — red with the *exact* predicted misleading number,
`processed = 2, want 1`, while zero plugins were actually disabled —
reproducing the real production symptom byte-for-byte. Restored exactly;
all three tests green again, `gofmt` clean, working tree clean.

**1 should-fix, folded in**: the audit log's `"action"` key duplicated
the audit row's own `action` column name (`disable_revoked`), reading as
if they described the same thing when the map key is actually the feed
entry's *requested* action. Renamed to `requested_action`.

**2 real findings, deliberately NOT folded into this fix — filed as
separate Backlog cards instead**, since both are pre-existing gaps
adjacent to, not caused by, this change, and fixing them here would
widen a security-sensitive PR beyond its own reviewed scope:
- ut-docs#2386: `internal/plugins/marketplace.Client.GetRevocations` (a
  confirmed-dead, zero-caller duplicate client per ut-docs#2225) has the
  identical wire-format bug, plus a worse one — its `LatestVersion int64`
  against a real `"latestVersion":"43"` string is a hard decode error,
  not a silent one. Not live risk today; worth fixing before ut-docs#2225
  ever resolves toward wiring this client up instead of deleting it.
- ut-docs#2387: `pos.env.example` and `internal/config`'s compiled-in
  default marketplace URL are both missing the `/api` prefix ut-cloud's
  gateway actually requires (`pos.env`/`pos.env.dev` already have it
  right) — a till running off either default gets a 404 on every sync
  tick regardless of this fix's own correctness, silently, since
  `SyncRevocations`'s own error path only warns.

**Cleared, checked explicitly by the reviewer**: no other caller of
`RevocationEntry`/`RevocationFeed`/`processRevocation` exists outside
`revocation.go`+its test; `GetRevokedPlugins` never read the dropped
`DeveloperID`/`RevokedAt` fields (has no production caller either,
ut-docs#2225); the new `(bool, error)` split cannot false-negative a
genuine disable (`found && IsActive && SetPluginState==nil` is always
`true`; a query/write error correctly returns `(false, err)`, retried
next tick); `plugins.id` is a `TEXT PRIMARY KEY`
(`internal/db/migrations/001_init.sql:391`) so `GetPlugin`'s `LIMIT 1`
can only ever match the one real row, ruling out a multi-version
ambiguity hazard; decoding `Action` without branching on it is safe,
nothing downstream infers from its presence; no disk writes in the diff;
no real client/shop name in any fixture (`com.test.*`/`com.acme.*`
only); backend-only with no UI surface, no `web/help/` topic mentions
revocation and no page route changed, so no manual update applies.

## Explicitly deferred

- Differentiating `"delete"` (real uninstall) from `"disable"` — the
  feed's `Action` field is now decoded but not yet branched on.
- ut-docs#2386 and ut-docs#2387 (above) — real, but pre-existing and
  outside this fix's own reviewed diff.
