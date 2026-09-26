# Review — replicas prune photos the main till no longer has (ut-docs#2785)

**Change:** the main's per-scope asset manifest (`items`, `categories`) sends
`"complete": true` only when its directory walk hit no error; an older main
never sends it → its replicas prune nothing. Replica side: migration **050**
`sync_asset_ledger` (scope, path, size, mtime, first-unlisted time, miss
count) records every file the replica downloaded (plus a backfill of
pre-upgrade downloads that match the manifest exactly). A file is pruned only
when **two consecutive complete manifests** miss it **and** ≥ 1 h passed, no
basket has lines, its size+mtime are still exactly as downloaded (a local
upload over it is kept), and it is a regular file inside the scope root
(symlinked files/parents refused). One `os.Remove` per file, empty parents
removed up to the root, ≤ 50 files per pull. **Safety valve:** a scope whose
complete manifest is empty while the replica holds downloads, or where > 5 files
and > 50 % of the scope would go at once, prunes nothing and logs a keyed
warning (Problems) — a main restored without photos can't wipe its replicas'
copies, which may be the last ones. Ledger writes for a pull in one
transaction. Help: multitill ×5 (+ the #2566 sentence ar/fa/tr never had),
categories ×5 stale sentence fixed. Main and standalone tills never prune.
Author: Opus 5.5. Reviewer: Fable.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | Help only in en/de (topic exists in ar/fa/tr; the #2566 sentence was also missing there) | added + translated, docs-shots re-run |
| 2 | major | No safety valve: an empty/restored main makes every replica delete all photos | valve (empty manifest / > 50 %) + keyed warning; 3 tests |
| 3 | minor | Grace wall-clock only (Pi without RTC) | ≥ 2 consecutive complete misses AND 1 h; relist resets (the test can't fail against the pre-fix code, which also needed two pulls — the counter makes it explicit) |
| 4 | minor | One UPDATE per row, no tx | `Apply` in one transaction per scope per pull |
| 5 | minor | Double stat per listed file | accepted |
| 6 | note | LAN manifest/download is plain http + bearer (pre-existing since #2566); prune adds nothing a MITM couldn't already do by overwriting | recorded |

**Checked, no issue (reviewer):** path containment (`..`, absolute, `\`, `:`,
symlink file/parent), TOCTOU needs the till's own write access, case/unicode
folding gives a hostile main nothing new, `complete` false on any walk error,
ledger persists across restarts, busy check nil-safe, migration 050 next free
number (unshipped, so its statements could still change — re-pinned), SQL
only in `internal/data`, table per-till (never bundled).

**Verification:** `go build ./...`, `go vet`, gofmt; `go test -race`
data/db + pages `Asset|Prune|SyncPull|2566|2785`; guards help-drift,
help-topics, data-access, migration-collision, docs-shots (`make docs-shots`
run alone). Not yet seen on the Pi/Windows replicas.

**Verdict:** safe to merge.
