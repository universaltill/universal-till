# Code review: purge cleans up self-update .bak rollback artifacts

**Card:** universaltill/ut-docs#257
**Date:** 2026-08-09
**Complexity:** easy — Dev inline (Sonnet), Review via an independent
fresh-context Sonnet subagent (isolated worktree). One review round;
nothing money/tax/data-loss/security-class was found, so a second round
wasn't earned per this pipeline's process-depth rule.

## What shipped

Now that `.deb` installs are self-updatable (ut-docs#151), a self-update
leaves rollback-backup artifacts on disk (`internal/selfupdate.go`'s
`Apply()`): the running binary renamed to `/opt/unitill/bin/unitill-pos.bak`,
and — if the archive shipped `web/` assets — the previous `web/` directory
renamed to `/opt/unitill/web.bak`. `packaging/scripts/postremove.sh`'s purge
path never cleaned these up, so a `apt remove --purge` on a box that had
ever self-updated left remnants of `/opt/unitill` behind.

Fix (`packaging/scripts/postremove.sh`, purge block only):

- Added `rm -f /opt/unitill/bin/unitill-pos.bak` and
  `rm -rf /opt/unitill/web.bak` inside the existing
  `if [ "$1" = "purge" ]; then ... fi` conditional, alongside the existing
  `rm -rf /var/lib/unitill /opt/unitill/data /etc/unitill`. `rm -f`/`rm -rf`
  on an absent target are silent no-ops (most installs never self-update, so
  this is the common case) — doesn't trip `set -e`.
- Non-purge (`apt remove`) behavior is unchanged: the two new lines sit
  strictly inside the purge-only conditional.

Regression coverage (`packaging/kiosk_setup_test.go`):

- New `TestPostremovePurgeCleansSelfUpdateBackups`, reusing the existing
  `chownDepthAt` helper (same one `TestPostinstallOwnsWholeInstallTreeForSelfUpdate`
  uses) to assert both new lines are present **and** nested one level deep
  inside the purge conditional — not merely present anywhere in the script,
  which is exactly how a prior guard in this file was shown to pass while
  sitting inside a comment (see this file's own header).

## Independent review (Sonnet, fresh context, isolated worktree)

Read the diff fresh, then independently verified every claim rather than
trusting the description:

- **TDD claim**: reverted only `postremove.sh` to `main`'s version, ran the
  new test — failed with the expected, specific assertion message (not a
  compile error). Restored the fix, reran — passed. Confirmed clean
  `git status` after restoring.
- **Own shell-level simulation** (built independently, not copied from the
  dev's description): `sed`-rewrote `postremove.sh`'s absolute paths into a
  scratch directory tree and ran it for real against dummy files —
  (a) purge with both `.bak` artifacts present removes exactly those, plus
  the pre-existing `data`/`var/lib/unitill`/`etc/unitill` cleanup, with no
  over-deletion (`/opt/unitill/bin` itself survives); (b) purge with no
  `.bak` files present (the common case) exits 0, no error; (c) plain
  `remove` (not purge) leaves all three artifacts untouched.
- **Full gate**, run independently: `go build ./...`, `go test ./...` (full
  suite, all packages green), all 3 CLAUDE.md guards
  (`guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`), and `sh -n postremove.sh` — all clean.
- **Correctness cross-check against the actual self-update code**: verified
  `exe` resolves to exactly `/opt/unitill/bin/unitill-pos` (via the systemd
  unit's `ExecStart` and nfpm's `bindir`/`binary` config in
  `.goreleaser.yaml`) and `webBase` resolves to exactly `/opt/unitill` (via
  `WorkingDirectory=/opt/unitill` in the unit) — both new `rm` paths match
  precisely, not just plausibly. Confirmed `rm -f` (file) vs `rm -rf`
  (directory) matches what `Apply()` actually creates for each.
- **Missed-artifact sweep** (went beyond the card's own description): the
  self-update temp download dir is already `defer os.RemoveAll`'d inside
  `Apply()` and never survives to be a purge concern; macOS's `.app`-bundle
  updater's own backup is transient by construction and moot anyway since
  macOS ships via `.dmg`, not `.deb` (`postremove.sh` never runs there);
  Windows is excluded from self-update entirely
  (`supportedFor("windows") == false`). No other platform or artifact gap.

**Verdict: safe to merge.** No blockers, no non-blocking nitpicks — diff is
small, precisely scoped (only the two intended files), correctly tested,
and the filenames/paths were confirmed exactly right against the real
self-update code rather than assumed.

## Verified beyond the automated suite

- Real shell execution against scratch-rooted copies of the actual script
  (not just static text assertions) — twice, independently, by both Dev/
  Tester and the reviewer — covering: both artifacts present, both absent,
  and plain `remove` vs `purge`.
- `sh -n` parse check (this script runs under `/bin/sh` on the target box;
  a bashism would only surface at install time on a real shop's device).
- Full `go build`, `go test ./...`, and all 3 CLAUDE.md guards, run twice
  (once before handoff, once independently by the reviewer).
- No real client/shop name anywhere in the diff; no secret-shaped literal.
  No `web/help/` update needed — packaging/removal script only, no
  shop-owner-visible surface. No README change needed — nothing the README
  claims changed.

## Safe-to-merge verdict

Yes — no blockers, no findings requiring a fix.
