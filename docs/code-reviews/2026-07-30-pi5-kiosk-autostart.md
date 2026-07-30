# Code review — Pi5 kiosk boot fixes (tty1/logind), auto-enable deferred

- **Date**: 2026-07-30
- **Branch**: `fix/pi5-kiosk-autostart`
- **Scope**: ut-docs/QUEUE.md field report — a Pi5 (192.168.1.162, Debian
  13 "trixie") never boots to the fullscreen kiosk. Diagnosed live via a
  manual systemd drop-in before this PR: (1) the `.deb` never enables the
  kiosk at all — entirely opt-in; (2) even run by hand, cage never gets
  the active VT (trixie boots with tty7 active, not tty1); (3) libseat
  picks the wrong backend under `PAMName=login` with seatd also installed.
- **Independent review**: different-model (opus) subagent, findings below.
  Found the original diff's headline feature (auto-enable on install)
  could not work at all — reverted per its findings, not shipped broken.

## What changed (final, post-review)

1. **`unitill-kiosk-setup.sh`**: the generated `unitill-kiosk.service` unit
   gets `ExecStartPre=+/usr/bin/chvt 1` (forces tty1 active before cage
   starts) and `Environment=LIBSEAT_BACKEND=logind` (stops libseat from
   picking the seatd backend under a `PAMName=login` session) — both
   proven live on the field Pi5 via a manual drop-in before landing here.
2. **`kbd` added** to the script's `apt-get install` line — the review
   found `chvt` (used by the new `ExecStartPre` above) comes from `kbd`,
   which nothing installed; without it the unit fails to start at all on
   any image that doesn't already carry it (confirmed absent in this
   sandbox, an ordinary Debian-based container).
3. **`KIOSK_USER` fallback**: still prefers `$SUDO_USER`; when unset, now
   auto-detects the first regular (uid 1000–59999) account instead of
   hardcoding `pi` — current Raspberry Pi Imager images ask for a custom
   username since Bookworm, no "pi" user by default anymore. Reads
   `/etc/passwd` directly rather than piping `getent passwd` through
   `awk '{exit}'` (the review found the early `exit` can SIGPIPE `getent`
   on a large/NSS-backed passwd source under this script's `pipefail`,
   silently aborting setup).
4. **New `scripts/ci/guard-kiosk-boot-fixes.sh`** (CI-wired), checking the
   two systemd directives and the `kbd` dependency are present on *active*
   (non-comment) lines, captured into a variable before any `grep -q` —
   not piped directly — so a `grep -q`'s early exit can't SIGPIPE an
   upstream `grep -v` and flip a genuine pass into a spurious guard
   failure under `pipefail`.
5. **`deploy/raspberry-pi/README.md`**: deprecation notice added (files
   kept, not deleted) — this older X11/autologin kiosk path was never
   fixed for trixie and several docs (now corrected, see below)
   incorrectly pointed to it as the current mechanism.
6. **`ut-docs`**: `guides/pos.md` and `hardware/diy-pos.md` repointed from
   the deprecated `deploy/raspberry-pi/` path to
   `packaging/linux/unitill-kiosk-setup.sh`.

## What was reverted, and why

The original diff (commit `1b30a29`) also had `postinstall.sh` auto-detect
Raspberry Pi hardware (`/proc/device-tree/model`) and auto-invoke the
kiosk setup on first install, so a shop owner would never run a command
by hand. **This does not work and was reverted** (commit `d0bd0ef`):

- **BLOCKER (reproduced)**: `unitill-kiosk-setup.sh` runs `apt-get
  update`/`install`. apt/dpkg hold the dpkg lock for the *entire*
  duration of a maintainer script (postinst), so a nested `apt-get`
  inside it deadlocks. Reproduced end-to-end with a throwaway `.deb`:
  `apt-get install ./pkg.deb` (the exact documented install command)
  fails with `E: Could not get lock /var/lib/dpkg/lock-frontend ... rc=100`,
  even with `DPKG_FRONTEND_LOCKED` set. On a real Pi5 this means the
  auto-enable step would print "Raspberry Pi detected…" and then die
  before ever writing the unit file — the exact manual step this feature
  existed to remove.
- **MAJOR (reproduced logic, not yet live)**: the auto-enable's only gate
  was "Pi hardware, no existing kiosk unit" — not "should this box be a
  till." `unitill-kiosk-setup.sh` runs `systemctl disable --now
  display-manager.service` and `systemctl set-default graphical.target`.
  Installing the `.deb` on a Pi running as a normal desktop (a developer
  box, or a shop's back-office Pi) would have killed the desktop session
  *during install* with no prompt and no opt-out.
- **MAJOR**: package removal (`preremove.sh`) never disables/removes the
  kiosk unit, reverts the default target, or re-enables the display
  manager — a `.deb` doing this automatically needs symmetric cleanup,
  which didn't exist.
- **MAJOR**: the gate (`kiosk service not already present`) means
  upgrading the `.deb` on the field Pi5 that reported this bug — which
  already has an old, broken kiosk unit from a prior manual run — would
  never regenerate it. The device that filed the report would not have
  been fixed by this change as originally written.

Fixing this properly needs a real redesign (package dependencies instead
of an `apt-get` call inside the maintainer script, e.g. a `oneshot`
systemd unit that runs after dpkg releases its locks, plus a real
opt-in/opt-out gate and removal-path cleanup) — logged as a new,
separately-scoped item in `ut-docs/QUEUE.md` with this review's findings
attached as design context, rather than forced into this diff or shipped
broken. What ships now — the two systemd fixes — is real, standalone
value: the *existing, documented* manual flow (`sudo
/opt/unitill/bin/unitill-kiosk-setup` + reboot) now actually works on
Debian 13 trixie/Pi5, which it did not before.

## Verification (pipeline side, re-verified independently — see below)

- New guard proven red/green across all three checks (chvt line, logind
  line, kbd dependency), including adversarial variants: commented-out
  with and without leading whitespace, fully deleted, and a "mere
  mention" of the exact matched string inside an unrelated `echo` (the
  same false-pass class this repo's `guard-emoji-font.sh` was caught with
  before) — all correctly fail; real, unmodified content passes.
- Pi-detection logic (before it was reverted, and independently for the
  general technique) exercised against fabricated `/proc/device-tree/model`
  content: a real Pi5 model string matches, generic x86 content and a
  wholly absent file do not, and the absent-file case was proven to emit
  a spurious shell-level "No such file or directory" without a `[ -r ... ]`
  guard — reproduced live in this very sandbox (an x86_64 container with
  no `/proc/device-tree/model` at all).
- `set -e` non-fatality of a failing kiosk-setup invocation proven by
  extracting the real block verbatim (via `awk` range-match, not
  retyped) into a standalone harness with a stub that exits non-zero —
  survived under `set -e`. (This code path no longer ships, but the
  technique and result are recorded since a future auto-enable redesign
  will need the same property.)
- `KIOSK_USER` fallback chain tested against all three branches
  (`SUDO_USER` set; unset with a real regular user present; unset with
  none) using the actual line from the file.
- `go build ./...` clean; `go test ./...` full suite green except one
  **pre-existing, unrelated** failure
  (`internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure`),
  confirmed identical against unmodified `main` via `git stash`/a fresh
  worktree (root-in-container permission-check artifact, matches this
  pipeline's own documented history, e.g. the batch 11 review).
  `bash scripts/ci/guard-data-access.sh` green. All touched/new shell
  files `bash -n` clean; `.goreleaser.yaml`/`.github/workflows/ci.yml`
  parse as valid YAML.

**Explicitly not done**: no real Pi5 hardware was reached this cycle (no
interactive channel to request Farshid's SSH access) — everything above
is sandbox-level logic/regression verification, the same standing caveat
this repo's PR #84/#87 kiosk fixes carried until Farshid physically
checked a real device reboot. A real Pi5 `.deb` install + reboot,
confirming the manual `unitill-kiosk-setup` flow now actually boots to
kiosk on trixie, is the outstanding real-hardware verification.

## Independent (opus) review — full findings and disposition

Re-verified every TDD claim personally (re-ran the guard revert/reproduce
cycles including variants the author hadn't tried, reproduced the
`/proc/device-tree/model` absent-file stderr live, ran the `set -e`
harness under both `dash` and `sh`, confirmed the pre-existing test
failure is identical on `origin/main`) — all held.

**Fixed, this PR:**
- BLOCKER: auto-enable can't run inside a dpkg maintainer script (`apt-get`
  deadlock) — reverted the auto-enable block entirely (see above).
- MAJOR: auto-enable could kill a working desktop with no opt-out — moot
  once reverted; the finding is preserved as design context for the
  follow-up.
- MAJOR: no removal-path cleanup for the kiosk service — moot once
  reverted; preserved as a requirement for the follow-up's design.
- MAJOR: upgrading wouldn't fix the reporting device — moot once
  reverted; preserved as a requirement (the follow-up must regenerate a
  stale/pre-fix unit, not just gate on "any unit present").
- MEDIUM: guard's postinstall check false-passed on a mere string mention
  inside an unrelated `echo` (demonstrated) — the postinstall checks were
  removed with the reverted feature; the guard now only checks
  `unitill-kiosk-setup.sh`, with checks anchored to real, non-comment
  lines.
- MEDIUM: `chvt`'s package (`kbd`) was never installed — added, guarded.
- LOW: `getent passwd | awk '{exit}'` could SIGPIPE-abort setup silently
  under `pipefail` — switched to reading `/etc/passwd` directly, no pipe.
- LOW: the guard's own `grep -v | grep -q` chain has the same SIGPIPE
  class, which can flip a true pass into a spurious guard failure under
  `pipefail` — fixed by capturing into a variable before matching (no
  live pipe between the two `grep`s).
- LOW: docs/release-notes asserted "no manual step" — reverted to
  describing the real (working) manual step, both in
  `.goreleaser.yaml` and the two `ut-docs` guides.

**Accepted as-is / queued:**
- NIT: the field Pi's LAN IP (`192.168.1.162`, RFC1918, not a credential)
  appears in commit `1b30a29`'s message, already pushed — left as-is per
  the reviewer's own assessment ("not worth blocking on").
- The auto-enable feature itself, properly redesigned (package
  dependencies or a oneshot post-dpkg unit, a real till-vs-desktop gate,
  removal cleanup, and a path to fix already-configured devices) — new
  `ut-docs/QUEUE.md` item, this review's findings attached as design
  context for the next BA/Architect pass.
- `architecture/packaging.md`, `zero-touch-setup.md`, `n150-migration.md`
  (ut-docs) still reference the now-deprecated `deploy/raspberry-pi/` path
  as if current — reviewer confirmed deferring these is reasonable (they're
  architecture/roadmap notes, not install instructions a shop owner
  follows) but flagged one real plan conflict worth carrying forward:
  `zero-touch-setup.md` proposes *extending* `deploy/raspberry-pi/` for a
  future flashable SD image, which now directly contradicts its
  deprecation — noted in the QUEUE.md follow-up.

## Final gate (after review fixes)

`bash -n` clean on all touched/new shell scripts; new guard proven
red/green across 5 adversarial variants + real content; `go build ./...`
clean; `go test ./...` green (one pre-existing unrelated failure,
confirmed identical on unmodified `main`); `bash
scripts/ci/guard-data-access.sh` green; YAML parses clean.

**Safe to merge**: the two systemd fixes + hardened guard + deprecation
notice + corrected docs. The auto-enable feature is explicitly deferred,
not shipped in any form (working or broken) in this PR.
