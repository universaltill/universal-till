# Code review: friendly `.deb` uninstaller (ut-docs#1083)

**Date:** 2026-08-29
**Author:** Scrum Master pipeline (`lane:cloud-54`), Dev via Fable subagent, review via Opus subagent
**Repo/PR:** `universal-till`, branch `feat/1083-deb-uninstaller`

## What shipped

A new `unitill-uninstall` command, shipped only with the `.deb` install:

- Offers a backup of shop data first (default yes; `--yes`/Enter accepts
  it), reusing `internal/db.Snapshot` (the same `VACUUM INTO` mechanism
  the in-app Backups page uses) after stopping `unitill-pos.service` so
  the WAL is checkpointed.
- Verifies the backup (non-empty, re-opens as SQLite, `PRAGMA
  integrity_check`) before anything is removed; any failure aborts with
  nothing touched and the till restarted.
- Separately asks whether to also delete shop data — keeping is the
  plain `apt remove` behaviour (default); deleting needs the operator to
  type `DELETE`, or the explicit `--purge-data` flag in scripted use.
- Delegates the actual removal to `apt-get remove`/`apt-get purge` —
  plain `apt remove`/`apt remove --purge` keep working unmodified.
- Non-interactive flags for scripted use: `--yes`, `--no-backup`,
  `--backup-to <dir>`, `--keep-data`/`--purge-data`, `--lang <code>`.
- i18n: reads `web/locales/*.json` directly (no HTTP server running),
  20 new `uninstall.*` keys in all four shipped locales (en/ar/fa/tr).
- Packaging: new goreleaser build id, nfpm contents entry, `postinstall.sh`/
  `postremove.sh` changes, `README.md` and `web/help/*/backups.md` updates.

40+ new tests (unit + full-flow with a fake exec runner and a real
migrated SQLite DB), TDD throughout.

## Independent review (Opus, isolated worktree)

**Verdict: not safe to merge as originally drafted** — one high-severity
should-fix plus three structural should-fixes, no blockers. All four
were fixed in this same cycle and independently re-verified (revert →
confirm the new/changed test fails for the right reason → restore →
confirm green) before this record was written.

1. **(high, fixed)** A failed `apt-get` (a held dpkg lock is the
   realistic trigger) after a *successful* backup left the till service
   stopped with no restart and no clear message — the shop's till
   silently down on top of nothing having been removed. Fixed: routed
   through the existing `abort()` path (loud message + best-effort
   restart) when the backup step had already stopped the service.
   New test: `TestRunAptFailureAfterBackupRestartsService`.

2. **(should-fix, fixed)** No regression guard for the pos→root
   escalation this design has to avoid (see below) — `packaging/
   kiosk_setup_test.go` already guards the analogous kiosk-script
   invariant (ut-docs#255) but hadn't been extended to the new binary.
   Fixed: added `TestUninstallerStaysOffPosWritableTree`, asserting (a)
   the nfpm `dst` for `unitill-uninstall` is outside `/opt/unitill`, (b)
   `ids: [linux, uninstall]` hasn't crept back into the `deb` nfpm entry
   (that's what would auto-place the binary in bindir), (c) the
   `postinstall.sh` PATH symlink's source matches the real nfpm `dst`,
   and (d) it isn't under `/usr/local`. Verified this test actually
   catches both regressions (temporarily reintroducing each, confirming
   the failure, then restoring).

3. **(should-fix, low, fixed)** The PATH symlink was under
   `/usr/local/bin`, which Debian Policy §9.1.2 reserves for the local
   admin, not packages. Moved to `/usr/bin/unitill-uninstall`.

4. **(should-fix, fixed)** `--backup-to` had no check against the
   directories the uninstall itself goes on to delete — `sudo
   unitill-uninstall --backup-to /opt/unitill/data/backups --purge-data`
   would write a *verified* backup into a tree the same run then
   `rm -rf`s. Fixed: `unsafeBackupRoot` refuses (before the service is
   even stopped) a `--backup-to` under `a.optDir` (`/opt/unitill`),
   `/var/lib/unitill`, `/etc/unitill`, or the configured `DataDir`.
   New test: `TestRunRefusesBackupToUnsafeDest`.

Nit also addressed: `TestShippedLocalesCarryAllUsedKeys` checked
translated output through `translator.T`, whose per-key English fallback
meant a key missing from `ar`/`fa`/`tr.json` could never actually fail
the test (the base `en.json` entry always covered it). Rewritten to read
each locale's raw parsed map directly. Verified it now genuinely catches
a missing key (temporarily deleted one from `ar.json`, confirmed the
test failed, restored).

Nits **not** independently important enough to hold this diff (recorded,
not silently dropped):
- `README.md` didn't originally document the non-interactive flags —
  added in this cycle too.
- `web/help/*/backups.md` says the tool "creates" a verified backup
  where it more precisely "offers" one (default yes) — consistent
  across all four locales; left as-is, not worth a four-locale text
  edit for this precision.
- `config_test.go` uses `UT_STORE_NAME=Task Runner` as test data — the
  vendor's own company name (already the nfpm `maintainer` field
  elsewhere in this repo), not a real customer/shop name. No action.
- `lang-pack-drift` will show advisory-only on this PR (20 new
  `en.json` keys) and would be blocking on a future push to `main`
  without the follow-up in the external `ut-plugin-language-{de,es}`
  packs — expected per this repo's own `CLAUDE.md`, not a gap in this
  PR.

## The pos→root escalation this design avoids — verified, not assumed

`unitill-uninstall` must run as root (stops a systemd unit, calls
`apt-get`). The first draft (from the Dev subagent) placed it at
`/opt/unitill/bin/unitill-uninstall` via nfpm's `ids:` auto-bindir
mechanism — the same directory `postinstall.sh` recursively `chown
pos:pos`'s for self-update (ut-docs#151), and `unitill-pos.service` runs
`User=pos`. That's a pos→root privilege-escalation vector: `pos` could
overwrite the binary and wait for `sudo unitill-uninstall`. Same class
of bug ut-docs#255 already fixed for the kiosk scripts (moved to
`/usr/lib/unitill`, never pos-writable).

Relocated the binary the same way: a per-arch `builds[].hooks.post`
copies each arch's build output to a fixed, non-version-suffixed staging
path (goreleaser's own dist layout embeds an unpredictable
GOAMD64/GOARM64 suffix that isn't safe to hardcode directly), referenced
by an nfpm `contents:` entry with `dst: /usr/lib/unitill/unitill-uninstall`
— dpkg-owned, root:root, outside `/opt/unitill` — and repointed the PATH
symlink. `uninstall` was removed from the `deb` nfpm's `ids:` list so it
no longer auto-places in bindir.

**This was verified empirically, twice** — once while making the fix,
once again after applying the review's four fixes — not just reasoned
about: downloaded a real goreleaser v2.11.2 binary, ran `goreleaser
release --snapshot --clean` (Linux builds + nfpm packaging only, Windows
CGO build skipped for lack of mingw in this sandbox), and inspected the
resulting `.deb` with `dpkg-deb -c`/`-e`:

```
-rwxr-xr-x root/root   6529208  ./usr/lib/unitill/unitill-uninstall
```

— present, correct mode/owner, and confirmed absent from
`./opt/unitill/bin/`. The postinst/postrm scripts inside the built
package were also inspected directly and match the source.

## Verified beyond automated tests

- Full `go test ./...` (all packages, including `internal/plugins` at
  its own 20-minute timeout) — green.
- `gofmt -l .`, `go vet ./...` — clean.
- Every CI-blocking guard in `.github/workflows/ci.yml`'s `build` job —
  all pass, including `guard-i18n.sh` (all 4 locales carry the full new
  key set) and `guard-help-topics.sh`.
- No UI/page surface touched (this is a terminal CLI, no new
  `internal/pages` route), so no browser/e2e run was needed — confirmed
  by inspecting the diff, not assumed.
- Real `goreleaser` packaging + `dpkg-deb` inspection, as above.
- TDD re-verified personally (not just taken on the Dev/Reviewer
  subagents' word) for every fix applied in this cycle: each of the four
  review findings' regression tests was confirmed to genuinely fail
  against the pre-fix code and pass after, via revert → run → restore.

**Not verified:** the full real-hardware cycle (install → backup →
purge → confirm nothing remains → reinstall → restore → confirm the
catalogue is back) on an actual Raspberry Pi / Debian box, per the
original card's "Verify" section — no hardware in this environment.
Covered instead by the automated full-flow tests plus the real
`goreleaser`+`dpkg-deb` packaging inspection above.

## Safe to merge

Yes, after the four review fixes above (all applied, tested, and
independently re-verified in this same cycle).
