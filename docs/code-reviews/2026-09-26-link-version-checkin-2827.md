# Review — link_version + conditional check-in (ut-docs#2827)

**Change (ut-cloud):** `merchant_organizations.link_version` (bigint, on the
regional store row — atomic `+1`, never read-modify-write), bumped by ent
hooks in the same transaction as every till-visible write: new store
directive (any kind), plan/status/expiry change (incl. the boot backfill),
device credential revocation (not rotation), legacy token retirement, main-till
move (only when the recorded main set changes). Guard test fails on any new
writer call site or an ent client built outside `internal/data`. `GET
/api/v1/stores/checkin` authorises first (revoked → 401 even with a matching
ETag), then `If-None-Match: "<store>:<N>"` → 304, else 200 `{link_version}` +
ETag. Hub hello/nudge read the stored counter; new nudge scopes: security on
revocation, entitlement on plan/status/expiry. **(universal-till):** the tick
GETs first; 200 → POST; 304 → skip the POST unless the till-state hash changed
or 10 min passed; a 304 confirms a cached entitlement; 401/429/503 fail the
tick like the POST; 404/405/5xx → POST as before and retry the GET hourly; a
newer socket version forces the POST. Authors: Opus 5.5 (two agents).
Reviewer: Fable.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | Rolling deploy: an old pod's writes don't bump; a 304 from the new pod delays a change up to the 10-min floor | accepted, documented in docs/operations.md (bounded; revocation still 401s) |
| 2 | minor | Till kept a higher version than a restored cloud → POST every tick forever | fixed (adopt the cloud's value); test `TestCheckinAdoptsALowerCloudVersion` (failed first) |
| 3 | minor | Config report flips the state hash twice → two POSTs per change | accepted (harmless) |
| 4 | minor | 304 still writes the throttled `last_used_at` | documented |
| 5 | minor | `accountChangesEntitlement` duplicated in data and tilllink | one exported `tilllink.AccountChangesEntitlement` |
| 6 | minor | First fleet save of a fresh store bumps 0→1 | accepted (one extra POST at pairing) |
| 7 | minor | Design's residency bullet: no data residency file | column sits on the regional store row — noted here |

**Checked, no issue (reviewer):** auth before ETag, store id bound to the
credential, no cross-store leak; ETag parsing (list, `W/`, quotes, `*` → 200);
same-tx bumps via `m.Client()`, rollback leaves no bump; no bump storm
(delivery/result updates, touches, rotation revokes don't bump); atomic
increment; bulk predicate updates go through hooks; the till never loses a
version (recorded only after the POST succeeds); a 304 can't resurrect a
changed entitlement (changes bump); hash excludes only `uptime_min`; old
till ↔ new cloud and new till ↔ old cloud both work.

**Verification:** ut-cloud `go vet ./...`, `go test -race ./...` (57 pkgs),
golangci-lint on touched packages; universal-till `go build ./...`,
`go test -race` cloudsync/cloudlink/entitlement, pages Cloud tests, guards.
**Deploy order:** ut-cloud first, then universal-till (release).

**Verdict:** safe to merge.
