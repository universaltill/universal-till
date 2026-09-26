# Review — relay cloud check-ins to replicas (ut-docs#2893)

**Change:** new one-way ADR-0114 frame `cloud_checkin` (main → replica:
`scopes` + `link_version`). The main till's cloud-link client records a
nudge (or a hello with a newer `link_version`) and, after the first
check-in that started after it and reached the cloud (`cloudsync`
`BeforeTick`/`TickStarting` + `AfterTick`), calls `Hub.RelayCloudCheckin`.
Each replica link holds one merged pending request, sent at most once per 5 s
(`Config.CloudCheckinEvery`); satellites never get it. The replica kicks its
own `cloudsync` loop through the capacity-1 `CloudSyncNow` channel. Older
replicas answer `unknown_type`, which the main ignores. ADR amendment:
ut-docs#2919. Author: Opus 5.5. Reviewer: Fable.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | Watchdog timeout then a held `cloud_checkin` write on a dead peer delayed teardown up to WriteTimeout (10 s) | fixed: `continue` after the timeout shutdown |
| 2 | minor | A replica reconnecting before its hello misses one relay | accepted, documented on `RelayCloudCheckin` (its 2-min check-in covers it) |
| 3 | minor | `helloFor` defaults an empty Role to "replica" — a future satellite client must set Role | documented on `RelayCloudCheckin` |
| 4 | minor | `relayArmed` left `armed` set when no relay was wired | fixed (cleared either way) |
| 5 | cosmetic | two copies of the scope bound | accepted |

**Checked, no issue (reviewer):** existing frames, hello, ping, bye, report
and backpressure unchanged (the live LAN link on tablet/Pi/Windows);
unknown-type both directions keeps the link up; a replica can't make the
main relay (inbound `cloud_checkin` ignored by the hub); no timers or
goroutines per relay (merged flag + existing 2 s ticker); a failed main
check-in keeps the relay armed, no storm (≤ 1 frame / 5 s / replica); a kick
never bypasses the replica's backoff/Retry-After (`kick = nil` after
errors); scopes clipped, deduped, capped at 8; no dead code.

**Verification:** `go build ./...`, `go vet`, gofmt; `go test -race -count=3`
fleetlink + cloudlink, `-race` cloudsync, pages `Sync|Link|Cloud|Tills|Checkin`;
docs-shots guard (surface hash only — Go-only wiring). Not yet seen on the
real tablet/Pi/Windows fleet → #2825.

**Verdict:** safe to merge after ut-docs#2919.
