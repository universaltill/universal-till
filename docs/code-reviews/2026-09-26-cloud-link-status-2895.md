# Review — cloud-link state on the till (ut-docs#2895)

**Change:** the Tills roster (`/tills`, 10 s poll) gains a "Cloud link" row
(hidden when the shop has no cloud enrolment; no new footer chip — sell-screen
space): Live · Connecting… · Periodic (tier, or not the main till) ·
Reconnecting (with the next attempt time) · Paused: not the main till ·
Paused: tier changed · Paused: cloud busy (with the Retry-After time when the
cloud gave one) · Stopped: credential revoked. `cloudlink.Client` gains
production accessors `State`/`Reason`/`NextAttempt`; `fleetlink.DialError`
carries the cloud's 403 error code so `not_main_till`/`tier_periodic`
refusals map to the right reason. The `status` frame to the cloud now also
lists enrolled LAN tills whose link is down, capped at 64 peers (live first).
Help `multitill.md` ×5. 20 new `tills.cloud_link.*` keys (en/ar/fa/tr).
Authors: Sonnet (build), Opus 5.5 (rebase onto #2827/#2893 + fixes).
Reviewer: Opus 5.5.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | Branch behind main (#2827 LinkVersion, #2893 relay) — conflicts in client.go | rebased; all of main kept + this branch's accessors; race tests green |
| 2 | major | Status frame "down" peers unbounded → > 16 KiB (ADR-0117 §7) → cloud closes, till redials, loop | capped at 64, live first; test with 200 tills |
| 3 | minor | 403 not_main_till/tier_periodic shown as "cloud busy" | DialError.Code parsed from the body, mapped |
| 4 | minor | "Next attempt" showed a past time while dialling | cleared when the timer fires |
| 5 | minor | 503 Retry-After hint wrong; failed dial after it stayed "busy" | own reason + hint; transport failure → Reconnecting |
| 6 | minor | Eligible till at boot showed "Periodic" | "Connecting…" |
| 7 | minor | Help scoped to the main till's page; the row shows on every till | sentence moved and reworded in all five languages |

**Checked, no issue (reviewer):** all states reachable; gate order enrolled →
not-main → tier; accessors atomic (race-tested); down peers carry only
`till_id`; one small SELECT per poll/status frame; template escaping and RTL
(existing styles only); ar/fa/tr translations real and consistent; removing
`export_test.go` correct (`State()` is production now).

**Verification:** `go build ./...`, `go vet ./...`, gofmt; race tests
cloudlink/fleetlink/cloudsync + pages subpackages; `internal/pages` full
without -race; guards i18n, help-topics, help-drift, core-neutral,
data-access; `make docs-shots` (124 passed, only manifest changed).
Screenshots of `/tills` at 1024×600 en + fa looked at (Periodic state live;
other states via the rendered-template test). New keys → de/es pack PRs after merge.

**Verdict:** safe to merge.
