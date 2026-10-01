# Review: a joined till's Settings rename renames itself, not the main till (ut-docs#3292)

- **Date:** 2026-10-01
- **Lane:** lane:cloud-24
- **Author:** Opus 5.5. **Reviewer:** Fable (independent, fresh context; `complexity:medium` per MODEL-ROUTING.md)
- **Card:** universaltill/ut-docs#3292

## The bug

`POST /api/settings/till-name` always saved `till.name` through
`saveShopSettings`. `till.name` is shop-wide (`data.SettingScope`), so on a
joined till (`sync.primary_url` set) the write was sent to the main till
(#2791), which renamed the **main** till. The joined till's own name is
`sync.till_name`. That is the key `enroll.DeviceName` reports to the cloud,
and the form never wrote it. The Settings field was also hidden on a joined
till (`{{ if .IsPrimaryTill }}`), but the endpoint could still be reached.

## What shipped

- `internal/pages/settings_page.go`: the handler picks the key that
  `enroll.DeviceName` reads for this till's role, the same way
  `cloudRenameTill` (#3272) does. A main till writes `till.name`, as before.
  A joined till writes `sync.till_name`. The write still goes through
  `saveShopSettings`, and `sync.*` is in `data.PerTillSettingPrefixes`, so
  it stays local with no write-through. That means it also works offline.
  The audit row (`till_name_changed`) uses that key as its entity id and
  carries `{name, key}`. The elevation gate is unchanged and still runs
  before the key is chosen. The view's `TillName` now comes from
  `deviceNameOrDefault`, and the template key `IsPrimaryTill`, which nothing
  else used, was removed.
- `internal/pages/till_name.go`: new `deviceNameOrDefault`, which returns
  `enroll.DeviceName` or the translated default.
- `web/ui/pages/settings.html`: the till-name field renders on every till.
  The stale comments here and in `sync_quarantine_page.go` were updated.
- `web/help/{en,de,ar,fa,tr}/multitill.md` step 9: one added sentence. A
  name changed in Settings on a joined till renames only that till, never
  the main till, and reaches the cloud at its next check-in.
- Tests (`settings_page_test.go`):
  - `TestTillNameEndpoint_JoinedTillRenamesItselfNotTheMainTill`: on a
    joined till with a bearer and a stub main till that answers 418, the
    rename gives 204. `sync.till_name` is trimmed and saved, `till.name` is
    unchanged, the main till gets zero requests, `DeviceName` changes, and
    there is one audit row on `sync.till_name`.
  - `TestTillNameEndpoint_MainTillAuditsTillNameKey`: the main till still
    writes `till.name`, leaves `sync.till_name` alone, and audits under
    `till.name`.
  - `TestSettingsPage_TillNameFieldShowsThisTillsOwnName` replaces
    `TestSettingsPage_TillNameFieldOnlyOnPrimary`. The field holds
    `till.name` on the main till, holds `sync.till_name` on a joined till
    (never the main till's name), and shows the translated default when the
    joined till has no name of its own.

## TDD evidence

- Before the fix, the joined-till handler test failed with `502 Can't reach
  the main till…`: the old handler tried to write `till.name` through to the
  main till. The view test failed with "expected the till-name field holding
  sync.till_name": the old view showed the main till's name, or no field.
- The reviewer re-verified this in a separate worktree. It reverted
  `settings_page.go` to its pre-fix version, saw both tests fail for those
  reasons, restored it, and saw them pass. After the bearer nit (below), the
  strengthened test was checked again against the old handler. It still
  fails, and now the write-through really reaches the stub.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | should-fix (pre-existing) | On the Tills page, a joined till's row keeps its pairing-time name (`tills.name`, written only by `InsertTill`) after any rename. This applies to both the main till's page and the joined till's own page. | Out of scope. It is exactly ut-docs#3294's card, which was already filed and is in Backlog. The handler comment that claimed the /tills page shows the name was corrected to point at #3294. |
| 2 | nit | The joined-till test set no `sync.bearer`, so `applySettingsOnMain` refused before making any request, and the `mainHits == 0` check was vacuous. The status assertion still caught the bug. | Fixed: the test sets a bearer, and was re-checked against the old handler. |
| 3 | nit | A local rename is not checked against `TillsRepo.NameTaken`, so two joined tills could end up with the same name. | Accepted. `cloudRenameTill` has the same gap, and the main till cannot check a joined till's name offline. Noted on #3294. |

The reviewer also confirmed these. The write key and the read key always
agree (`tillFollowsMain` and `isReplica` both trim `sync.primary_url`). No
admin pull, heartbeat or `StageReplicaIdentity` overwrites `sync.till_name`.
On promotion, `ClearReplicaIdentity` copies `sync.till_name` into `till.name`,
so a renamed joined till keeps its new name. The nav chip on a joined till
reads `sync.till_name`, so the field, the chip and the cloud agree. A blank
name writes nothing, as on a main till. There are no new strings, no
left/right CSS and no `showModal`, and the translations match the English.

## Verified beyond automated tests

I ran a throwaway till (`UT_AUTH=off`, fresh data dir) set up as a joined
till: `sync.primary_url` pointing at an unreachable port, `till.name` set to
"Main Counter" and `sync.till_name` set to "Back Office". I drove it in
Chromium. Settings → Tills showed "Back Office". Saving "Terrace" returned
204. In the DB, `sync.till_name` was "Terrace", `till.name` was still "Main
Counter", and there was one audit row
`sync.till_name {"key":"sync.till_name","name":"Terrace"}`. That is the
offline case: the main till could not be reached. I looked at screenshots of
the Tills card at 1024×600 (en, light) and at 360px (fa, RTL). The field
lays out the same as the register picker next to it. At 360px the card's
heading sits under the sticky top bar, which is the existing #3148. Not
checked: dark theme and real touch hardware.

## Gate

- `gofmt -l .` and `go build ./...` are clean.
- `go test` was run in CI's form: the package list from `ci.yml`, plus
  `go test -timeout 20m ./internal/pages` and `-race ./internal/enroll`.
- The `ci.yml` build-job guards pass, with three exceptions that come from
  this environment: `guard-shellcheck-version` (no shellcheck binary here,
  and no shell scripts were touched), `guard-deadcode-baseline` (it cannot
  analyse `cmd/unitill-desktop` without GTK headers, and the flagged
  `internal/logging` code is untouched), and docs-shots, which is fixed by
  committing `make docs-shots`.
- A first `go test -race ./...` run timed out in `internal/pages` and
  `internal/db` at Go's default 10 minutes, while the guards were running on
  the same machine. CI does not run those packages with `-race`.

## Verdict

Safe to merge.

## Deferred

- ut-docs#3294 (existing card): the Tills page should show a joined till's
  current name. Finding 3 was added there.
