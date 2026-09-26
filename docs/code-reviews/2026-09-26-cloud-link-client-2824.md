# Review — till cloud-link client on the main till (ut-docs#2824)

**Change:** new `internal/cloudlink` (dial gate, `wss` to `<cloud base>/v1/tills/link`
with the device bearer and no Origin, hello with last `link_version`, nudge →
single-flight check-in kick, close-code handling, redial jitter + backoff,
`status` frames on connect/change/≤30 s, `sale` frames only while `live_view` is
on); `fleetlink/session.go` so the peer loop carries another protocol's
messages (reused, not forked); `cloudsync` `Hooks.Kick` + `Hooks.AfterTick`;
`pages/cloud_link.go` wiring + gate + sale/status mapping; `entitlement.CloudLink`
restored from 44bedbd41 (now has a caller). ADR-0117 task 4.
Author: Opus 5.5. Reviewer: Fable.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | Refund frames unreachable (refunds never publish) | deferred with card: **#2894** (refund + replica sale frames) |
| 2 | minor | A kick buffered during backoff fired right after → one redundant POST | fixed: pending kick drained as each check-in starts; test |
| 3 | minor | 1013/4029 and admit-then-cut loops redialled every ≤60 s forever | fixed: 1013/4029 wait for a check-in; links shorter than 30 s grow the backoff; 3 tests |
| 4 | minor | `CheckedIn(err==nil)` lifted 4010/4011 waits on a tick that never reached the cloud (unregistered till returns nil early) | fixed: `AfterTick(ctx, contacted, err)`; only real contact lifts a wait; test |
| 5 | minor | Sale `Time` was publish time | fixed: sale's `created_at` (= `completed_at`), fallback now |
| 6 | minor | Gate test "backoffice" case exercised nothing | replaced with "backoffice display on a replica → no dial" |
| 7 | minor | Bye reason always `shutdown` | accepted: self-update/restart re-exec in place without cancelling the context; needs a before-restart hook (noted on #2895) |
| 8 | minor | No status surface | deferred: **#2895** |

**Checked, no issue (reviewer):** wire matches ut-cloud `internal/tilllink`
field-for-field (hello, nudge, live_view, sale, status, envelope v1, close
codes 4003/4010/4011, `?store_id`, bearer, no Origin); TLS via the default
transport (WebPKI, redirects refused), `ws://` loopback only incl. `[::1]`;
gate (replica with cached realtime, periodic, on_demand, stale, unenrolled →
no dial; demotion closes with `bye`); 401/4003 no dial until the bearer
changes; Retry-After honoured; sale path nil-safe and non-blocking, summary
only, minor units, negative total on return; single-flight kick proven (max
concurrency 1, exactly one extra run); **fleetlink refactor byte-identical for
the LAN link** (existing hub/client tests `-race -count=3`); shutdown/leaks;
AC5 relay met through the existing 1 s `sync_admin_version` watch (direct
relay of entitlement/security nudges = #2893).

**Verification:** `go build ./...`, `go vet`, gofmt clean; `go test -race
-count=3` cloudlink + fleetlink + entitlement; `go test -race` cloudsync; pages
CloudLink tests; guards data-access, kiosk-engine, i18n, page-http-error,
demo-env, plugin-menu-read, plugin-settings-bump. Not run locally: golangci-lint
(local binary can't load `math/rand/v2`, same on main) and whole-program
deadcode (needs GTK headers for `cmd/unitill-desktop`) → CI.

**Verdict:** safe to merge. End-to-end against the live hub + devices = #2825.
