# Kiosk screen capture had no sources — Wayland screencast portal wiring (ut-docs#395, PR #210)

## What shipped

Field report: pressing screenshot in the bug-report panel on the kiosk opens
Chromium's `getDisplayMedia` picker with **nothing to select**, so capture
can't complete. Root cause (diagnosed live on field hardware): the kiosk
runs Chromium under `cage` (a single-app Wayland compositor) with
`--ozone-platform=wayland`, which delegates `getDisplayMedia` to
`xdg-desktop-portal`'s ScreenCast interface — and no portal was ever
installed or configured, so there was nothing to enumerate.

Fix (three parts):
- `packaging/linux/unitill-kiosk-setup.sh` — installs
  `xdg-desktop-portal`/`xdg-desktop-portal-wlr` and their runtime
  dependencies, and writes two system-wide config files selecting the wlr
  backend and auto-selecting the only output (`chooser_type=none` — no
  chooser dialog the kiosk cannot display).
- `packaging/linux/unitill-kiosk-launch.sh` — the portal is D-Bus-activated
  and does not inherit the launch script's environment, so
  `XDG_CURRENT_DESKTOP=wlroots` is exported **and** pushed via
  `dbus-update-activation-environment` before Chromium starts.
- `scripts/ci/guard-kiosk-launch-flags.sh` — CI guard asserting both lines
  survive.

This PR does not close ut-docs#395 in full: it fixes the "no sources at
all" root cause only. Thumbnails, a non-modal draggable panel, and keeping
the till usable underneath are explicit follow-on scope, unchanged from the
PR's own description.

## Why this went through a second grooming pass

This PR was opened by a prior pipeline cycle that ended (cold-context cycle
boundary) before the Reviewer step ran — no `docs/code-reviews/` record
existed on the branch, and the linked issue (#395) was still sitting at
`status:ready`, never claimed. Caught by this cycle's step-0c stale-PR
sweep; claimed the issue, then ran the independent review that should have
run before the PR was opened for review.

## Independent review (fresh subagent, `complexity:hard` → Opus, deliberately not Fable)

**Verdict: yes-with-fixes-below.** The diagnosis was correct throughout and
the mechanism matched upstream's own documented recommendation verbatim —
nothing was wrong in principle. Six should-fix findings, all addressed
before merge:

1. **The new CI guard was satisfied by a comment, not code.** Verified by
   execution: commenting out both fixed lines (text still present, just
   `#`-prefixed) left the guard printing `✓ screencast portal environment
   wired` — the exact anti-pattern `packaging/kiosk_setup_test.go`'s own
   header already documents being caught and fixed once before ("the first
   version of this test used whole-file substring checks, and the
   independent review proved every fix could be reverted while a comment
   mentioning it kept the test green"). Fixed: the guard now strips
   comments/blanks before grepping (`code_lines()`), and the same mutation
   (comment out both lines) now fails it correctly — re-verified by
   re-running the exact mutation.
2. **PipeWire and `dbus-bin` were never installed.** The portal package
   alone advertises ScreenCast, but stream *creation* needs a running
   PipeWire daemon + session manager (`pipewire`, `wireplumber`), and
   `dbus-update-activation-environment` (called by the launch script) ships
   in `dbus-bin` on Debian trixie — none of which `--no-install-recommends`
   pulls in. This is the identical "happened to be present on the field
   devices, nothing guaranteed it" gap the PR's own commit message uses to
   justify installing the portal in the first place. Fixed: `pipewire
   wireplumber dbus-user-session dbus-bin` added explicitly to the install
   line, same file.
3. **`dbus-update-activation-environment`'s stderr was discarded
   (`2>/dev/null`).** `|| true` alone already lets the kiosk boot on
   failure; piping stderr to `/dev/null` on top of that leaves zero trace
   anywhere if the call fails (e.g. `dbus-bin` missing → "command not
   found"), which is the exact silent-failure shape ut-docs#395 itself was
   filed about. Fixed: dropped `2>/dev/null` — the systemd unit already
   sets `StandardError=journal`, so a real failure now reaches
   `journalctl -u unitill-kiosk` for free.
4. **The per-user portal config hardcoded `/home/$KIOSK_USER` and its
   primary group.** `install -d -g "$KIOSK_USER"` assumes a group named
   after the user; if it doesn't match, `install` fails and — under
   `set -euo pipefail` — aborts the *entire* kiosk setup partway through,
   before the emoji font, group memberships, or the kiosk service unit
   itself are installed. Fixed: moved both portal configs to system-wide
   `/etc` paths (`/etc/xdg-desktop-portal/wlroots-portals.conf`,
   `/etc/xdg/xdg-desktop-portal-wlr/config` — the latter is
   `xdg-desktop-portal-wlr`'s own documented system-wide search path), which
   needs no user lookup, no group guess, and no `chown` at all.
5. **The portal apt install ran before Chromium, with no fallback, under
   `set -e`.** Any transient apt/mirror failure on this one package would
   have aborted setup before Chromium — and therefore the POS itself — ever
   installed, for a screenshot dependency. Fixed: moved after the Chromium
   install block, and wrapped in `if ! apt-get ...; then echo "⚠ ..." >&2;
   fi` so a failure here degrades to "no screen capture" rather than "no
   till."
6. **Zero test coverage for the new setup-script config work** (the review
   explicitly flagged `packaging/kiosk_setup_test.go` — home to this exact
   file's own anti-revert precedent — as untouched by the PR). Fixed: added
   `TestKioskLaunchWiresScreencastPortalEnvironment` and
   `TestKioskSetupConfiguresScreencastPortal`, both using the file's
   existing `codeLines`/`anyLineContains`/`heredocBlock` helpers (same
   comment-can't-satisfy-it property as the shell guard, at the Go test
   layer too, so `go test ./...` catches a regression independently of the
   shell guard running at all).

One nit taken: `default=wlr;gtk` in `wlroots-portals.conf` named a fallback
backend (`xdg-desktop-portal-gtk`) that isn't installed — narrowed to
`default=wlr`. One nit deliberately not acted on: the PR's "no picker"
framing describes `xdpw`'s own chooser only; Chromium's `getDisplayMedia`
consent dialog is a separate layer this diff doesn't touch and wasn't
verified — noted here so it isn't carried into #395's remaining UI work as
settled fact.

One item the review flagged as needing a clean-image check, not resolved
here: whether Debian's `xdg-desktop-portal-wlr` package Depends (vs.
Recommends) on `pipewire` upstream — egress-blocked from this review's
sandbox to confirm against packages.debian.org. Installing `pipewire`
explicitly (finding 2) makes this moot for `--no-install-recommends`
either way, so it doesn't block merge, but it's worth a real first-boot
verification on field hardware rather than assumed.

## Independently re-verified myself (mutation testing, not just reviewer's word)

- Reverted-via-comment mutation on the launch script (finding 1): guard
  correctly fails with the new exit code, confirmed correctly passes when
  restored.
- Removed `pipewire` from the setup-script install line: the new Go test
  fails with the exact expected message; restored, passes.
- Removed the `if ! ...; then / fi` non-fatal wrapper around the portal
  install (finding 5): the new Go test fails with the exact expected
  message; restored, passes.

## Verified beyond automated tests

- `bash -n` on both shell scripts (parse-clean).
- `go build ./...`, `go vet ./...` clean.
- `bash scripts/ci/guard-data-access.sh`, `bash
  scripts/ci/guard-kiosk-launch-flags.sh` — both green.
- `go test ./...` — clean except one pre-existing, unrelated failure
  (`internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure`, caused by
  this sandbox running as root and bypassing a read-only-directory
  permission the test relies on — same failure already documented
  reproducing identically on `main` HEAD in this repo's own prior review
  records, e.g. `2026-08-07-kiosk-status-bar-update-chip-dead-link.md`; not
  touched by this diff).
- `gofmt -l .` flags four pre-existing files, none touched by this diff.

## Checked and found fine (so it isn't re-litigated)

- Idempotency: `apt-get install -y`, `install -d`, `cat >` (truncate, not
  append) are all safe to re-run.
- Ordering: `KIOSK_USER` resolution (still needed for the systemd unit's
  `User=` and group memberships) happens well before its remaining uses.
- Manual/docs: `web/help/en/bug-reporting.md` already describes the
  post-fix behavior ("press 📷 Take screenshot..."); no kiosk-provisioning
  runbook exists separate from the setup script's own header comments, so
  nothing else needed updating. Device provisioning, not shop-owner-facing
  in-app UI.
- No real client/shop name, no secret-shaped literal anywhere in the diff.
- Distro/package names (`xdg-desktop-portal`, `xdg-desktop-portal-wlr`,
  `pipewire`, `wireplumber`, `dbus-user-session`, `dbus-bin`) are correct,
  stable Debian binary package names across Pi OS/Debian — no
  chromium-style fallback needed for the names themselves.

## Explicitly deferred (new Backlog cards, not this diff's scope)

- The remaining ut-docs#395 acceptance criteria (thumbnails in the capture
  panel, a non-modal draggable panel, keeping the till usable underneath) —
  unchanged from the PR's own stated scope; issue stays open, not moved to
  Done.
- Real first-boot verification on field hardware that PipeWire's ScreenCast
  stream actually completes end-to-end with the newly-added packages (the
  portal wiring itself is now verified at the config/script level, not
  live capture on real hardware — the PR's own original "verified on real
  hardware" claim covered the pre-fix symptom, not this fix's package
  additions).

## Safe to merge

Yes. Feature branch `fix/kiosk-screencast-portal`, merged via `merge` (not
squash/rebase, per this pipeline's standing merge-method rule, ut-docs#250).
