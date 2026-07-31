# Code review — Pi kiosk autostart on fresh .deb install (2026-07-31)

**Branch:** `fix/pi-kiosk-autostart-firstboot` (ut-docs#6, p1 field bug)
**Scope:** packaging scripts + CI guard + regression tests + docs; retires `deploy/raspberry-pi/`.

## What shipped

Encodes the three root causes diagnosed live on the field Pi5 (Debian 13/trixie, 2026-07-30):

1. **Fresh .deb never enabled the kiosk** → `postinstall.sh` now stages a `unitill-kiosk-firstboot.service` oneshot on a Pi (apt can't run inside the dpkg transaction), which runs `unitill-kiosk-setup --auto` on first boot.
2. **trixie boots with active VT on tty7; cage on tty1 never got an active logind session** → generated unit gains `ExecStartPre=+/usr/bin/chvt 1` (kbd now installed for chvt).
3. **seatd's presence made libseat pick the seatd backend under PAMName=login → "Broken pipe"** → unit gains `Environment=LIBSEAT_BACKEND=logind`; setup no longer installs seatd.

Also: `--auto` non-interactive mode (SUDO_USER → uid-1000 → `pi` → creates `utkiosk` as last resort); the stale, contradicting X11 path `deploy/raspberry-pi/` retired; README/.goreleaser/app.css docs updated.

## Independent review (different model: Opus) — 3 blocking, 8 should-fix, 9 nits

The review ran build/vet/tests/shellcheck itself and **proved two of its blockers by construction**:

- **B1 (CI-red):** `guard-emoji-font.sh` hard-coded the deleted `deploy/raspberry-pi/install.sh` — the branch failed CI. *Fixed:* guard now covers the two remaining Linux install paths; regex widened to allow flagged `apt-get -o … install` lines.
- **B2 (false-pass tests, proven by mutation):** the first regression tests used whole-file `strings.Contains`, and every asserted token also appeared in a comment — the reviewer reverted all three fixes and the tests stayed green. *Fixed:* tests now extract the systemd heredoc blocks and assert on non-comment lines only; apt asserts anchor to real `apt-get … install` lines. Re-verified by re-running the reviewer's exact mutation battery — all four mutations now fail the tests.
- **B3 (consent/scope):** plain `apt install`/upgrade would have kiosk-ified *any* Pi — including Pi OS Desktop (disabling the user's desktop) and every upgraded field Pi that never opted in. *Fixed:* staging now gates on fresh install only (`$1=configure` with empty `$2`), Debian-family os-release (Ubuntu-on-Pi's snap chromium doesn't run under cage — was S5), and no enabled display manager; desktop boxes stay manual-opt-in.
- **S1 (offline first boot dead-end):** oneshot had no retry; an offline shop's first boot would leave a black console forever. *Fixed:* `Restart=on-failure` + `RestartSec=60` + `TimeoutStartSec=30min`, and apt calls carry `DPkg::Lock::Timeout=600` (apt-daily holds the lock right after network-online).
- **S2 (false success):** `systemctl start || true` + marker-in-ExecStartPost recorded a dead kiosk as done, disabling all retries. *Fixed:* `--auto` starts `--no-block`, polls `is-active` up to 30s, exits non-zero (no marker, unit stays armed) if the kiosk didn't come up.
- **S3 (user dead-end):** no uid-1000/`pi` → permanent silent boot-loop failure. *Fixed:* `--auto` creates a dedicated `utkiosk` login user as last resort.
- **S4 (removal orphans):** kiosk units aren't package-owned; `apt remove` left cage restart-looping with no getty. *Fixed:* preremove disables both units; postremove removes them, restores `multi-user.target`, purges `/etc/unitill`; firstboot unit gains `ConditionPathExists` on the setup binary.
- **S6 (undo didn't stick):** manual setups now write the done-marker too, so the staged firstboot can't silently re-enable an undone kiosk; header documents the full undo.
- **S7 (docs):** README, `.goreleaser.yaml` release footer, setup header, `app.css` comment all updated to the new behavior (incl. the `/etc/unitill/no-kiosk` opt-out).
- **S8 (cross-cutting consequence):** retiring `install.sh` removed the only self-updatable Pi provisioning route (#147's writability probe keyed on its `chown pos:pos`). Deliberately NOT resolved here — logged as ut-docs#151 (decide .deb ownership vs apt-repo updates).
- Nits applied: `$1=configure` guard, orphan condition, sh-parse CI test for the maintainer scripts, stale references. Not applied: none rejected.

## Verification beyond automated tests

- **Live on real trixie Pi hardware** (Pi4 at 192.168.1.167 — same Debian 13 as the diagnosis, with seatd installed, i.e. exactly failure-scenario (3)): final script run end-to-end, unit regenerated with both fixes, service restarted clean, Chromium fullscreen, active VT=1, no libseat errors, POS answering, done-marker written.
- Mutation battery: chvt line, LIBSEAT line, kbd package, `--auto` parse, upgrade-guard — each individually reverted fails its test.
- `go build/vet/test ./packaging/` green; all five `scripts/ci/guard-*.sh` pass; `sh -n`/`bash -n` clean (now also a CI test).
- **Honestly not verified:** the full postinstall→firstboot flow on a *fresh flash* (the Pi5 at .162 is offline/powered down). The staged-unit content and gating are regression-tested, and every runtime component it invokes was live-verified — but the ticket's "re-test on a fresh .deb install" tail remains; noted on the issue for when hardware is back (field-test card #21).

## Verdict

**Safe to merge.** The p1 gap (fresh Pi till boots to a console) is closed with the exact fixes proven on-device, review findings all fixed and re-verified, and the risky auto-conversion surface deliberately narrowed to fresh Pi OS Lite appliance installs.
