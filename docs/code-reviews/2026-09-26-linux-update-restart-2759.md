# Review — an in-app update ends up running the new binary (ut-docs#2759)

**Finding while building:** the systemd server (user `pos`) that applies the
update already re-execs the new binary in place (same PID), so the card's
`NRestarts=0`/old `ActiveEnterTimestamp` evidence was not proof the old
binary kept serving. The real gaps: a failed/stuck re-exec was only logged,
"Update now" reloaded on the first `/healthz` 200 (which the old process also
answers), and nothing checked the running version.

**Change:** after the swap, `unitill-pos --version` (new; prints the version
and exits before any config/DB/plugin/network work) is smoke-run with a 10 s
timeout; a binary that can't start is rolled back from `.bak` (binary and
`web/`), a Problem says "update could not start; kept vX", and nothing is
signalled. A marker `/opt/unitill/bin/.unitill-update-pending` (0600, regular
file only, ≤ 4 KiB) records the swap; if the re-exec fails under systemd
(`INVOCATION_ID`) the server re-checks the smoke run and only then SIGTERMs
itself so `Restart=always` starts the new binary; outside systemd it stays up
with a "restart pending" Problem; a 60 s watchdog raises the same if the old
process is still alive. At startup the marker is reconciled (cleared when the
running build ≥ recorded, dropped when older than the binary's change time,
skipped for `dev` builds). `GET /api/update/status` (manager-gated,
demo-blocked, `no-store`) reports running/installed/restart_pending; the
status bar reloads only once the running version equals the one installed.
`updates.Newer` handles prerelease suffixes. `postremove.sh` removes the marker
on purge. No polkit, no sudo, no unit change (unit pinned by a test).
Author: Opus 5.5. Reviewer: Fable.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | SIGTERM fallback could turn "old build still serving" into an offline till (unrunnable binary → endless Restart=always loop) | smoke run + rollback from .bak before any signal; tests incl. "signalled into a binary that cannot start" |
| 2 | minor | marker read followed symlinks, no size cap | Lstat + regular file + 4 KiB cap |
| 3 | minor | `dev` builds / prerelease compare | skipped for dev; `Newer` handles `-rc`, `+build` |
| 4 | minor | UI trusted `restart_pending=false` | reload only when running == installing version |
| 5 | minor | stale marker after a .deb downgrade; not purged | dropped when older than the binary's mtime/ctime; purged |
| 6 | cosmetic | duplicate watchdog Problem | same key, deduped; silent after rollback |

**Accepted:** after an exec failure + rollback, hardware plugins stopped by the
pre-restart hook stay stopped until the till restarts (pre-existing; the
Problem says to restart).

**Verification:** `go build ./...`; vet linux/windows/darwin;
`go test -race -count=3 ./internal/selfupdate/...`; pages `Update`, full pages
+ app + updates + packaging; a real `--version` build (prints, exits 0, no
files) and a wrong-arch binary failing the smoke run; guards i18n,
core-neutral, data-access, docs-shots. **Device test on Pi5-1 after the next
release** — steps on the card.

**Verdict:** safe to merge. New key `status.update_restart_pending` → de/es packs after merge.
