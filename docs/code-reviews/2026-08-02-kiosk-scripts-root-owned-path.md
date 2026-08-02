# Code review: kiosk scripts moved to a root-owned path (ut-docs#255)

**Date:** 2026-08-02
**Scope:** `.goreleaser.yaml` (nfpm `contents:` + release notes),
`packaging/scripts/postinstall.sh`, `packaging/linux/unitill-kiosk-setup.sh`,
`README.md`, `packaging/kiosk_setup_test.go` (new/extended tests).
**Trigger:** ut-docs#255, raised by the independent review of ut-docs#151
(`docs/code-reviews/2026-08-02-deb-selfupdate-ownership.md`). That fix made
the whole `/opt/unitill` tree writable by the `pos` service user (required
for `selfupdate.Supported()`), but `.deb` installs also ship two helpers
inside that same tree that get executed with elevated privilege:
`unitill-kiosk-setup` (root — `unitill-kiosk-firstboot.service` has no
`User=`, and the manual `sudo unitill-kiosk-setup` flow is root too) and
`unitill-kiosk-launch` (the kiosk user, not root — only
`unitill-kiosk.service`'s `ExecStartPre=+chvt` is root). `pos` — running the
network-facing HTTP server plus in-process third-party WASM plugins — could
plant a script either one would then execute.

## Fix

Both scripts move from `/opt/unitill/bin` to a new, root-owned,
dpkg-managed `/usr/lib/unitill/` — outside the subtree `postinstall.sh`'s
`chown -R pos:pos /opt/unitill` touches. Updated: nfpm `contents:` dst paths,
the `unitill-kiosk-firstboot.service` heredoc's `ExecStart=`/
`ConditionPathExists=`, `unitill-kiosk-setup.sh`'s own `install -D`
destination for the launch script and the `unitill-kiosk.service` heredoc's
`ExecStart=`, plus the release-notes footer and `README.md`'s operator
instructions. No change to kiosk-staging logic itself — path/ownership
only. The unrelated `archives:` (tar.gz) section's copies of these same two
scripts are untouched (no root/pos split in that flow).

## Verification (self, before independent review)

- TDD: wrote `TestKioskHelpersStayOffPosWritableTree` first (asserts the
  property "not under `/opt/unitill`" for all six coupled path references —
  nfpm dst ×2, firstboot unit `ExecStart`/`ConditionPathExists`, the
  `install -D` destination, and `unitill-kiosk.service`'s `ExecStart`),
  confirmed it failed against the pre-fix script with real, specific error
  messages naming each offending path, then implemented the path move and
  confirmed it passed.
- Mutation-checked all 6 of the test's assertion paths individually
  (reverted each literal back to `/opt/unitill/bin/...`, confirmed the
  specific correct failure, restored) — all 6 failed correctly.
- `go build ./...`, `go vet ./...`, `gofmt -l packaging/`: clean. Full
  `go test ./...`: clean except the same pre-existing, unrelated
  `internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure`
  root-sandbox artifact already documented in the ut-docs#151 review
  (confirmed unrelated: no import edge to this diff, this diff touches no
  Go source outside `packaging/kiosk_setup_test.go`).
- `bash scripts/ci/guard-data-access.sh`, `guard-i18n.sh`,
  `guard-kiosk-launch-flags.sh`: all green (no SQL, no user-facing strings,
  `--password-store=basic` still present — this diff never touches the
  launch script's Chromium flags, only where it's installed).

## Independent review (Opus, fully independent re-verification)

Re-ran the full gate itself, independently re-verified the TDD claim by
mutation-testing all 6 of the new test's assertion paths (not just a
sample) — each failed with the correct, specific message; restored the
working tree to a byte-identical `git diff` afterward (confirmed via
sha256 of every touched file).

**Found and fixed two real gaps:**

1. **(Medium-high) The fix as submitted only reached fresh installs.**
   `unitill-kiosk-firstboot.service` and `unitill-kiosk.service` are not
   dpkg-managed (both written by heredoc at install/setup time, not shipped
   as package `contents:`), so moving the *package's* copy of the scripts
   does nothing for a box that installed or ran kiosk setup before this
   fix — its on-disk unit files still say `/opt/unitill/bin`, untouched by
   dpkg upgrading the package underneath them. Concretely: the stale
   firstboot unit's `ConditionPathExists`/`ExecStart` still point at
   `/opt/unitill/bin/unitill-kiosk-setup`, which the still-unconditional
   `chown -R pos:pos /opt/unitill` keeps pos-writable — the exact
   pos→root path this ticket exists to close survives upgrading on every
   box that installed before it. Secondary: a Pi whose kiosk was already
   set up would lose its launcher's install path entirely once nfpm stops
   shipping anything at the old location.

   **Fixed**: added an unconditional (every invocation, positioned before
   the fresh-install-only `is_pi_appliance` gate — same requirement the
   existing `chown` line already holds itself to) migration block in
   `postinstall.sh`: if `/opt/unitill/bin/unitill-kiosk-launch` exists and
   `/usr/lib/unitill/unitill-kiosk-launch` doesn't yet, copy it forward
   first; then, if either stale unit file exists and still references the
   old path, `sed -i` it to the new one in place. Idempotent (every check
   is a no-op once migrated). TDD'd with a new test,
   `TestPostinstallMigratesStaleKioskUnitsFromBeforeUsrLibMove`, confirmed
   failing pre-fix with the real gap description, passing after. Beyond
   the static-text test (this repo's established pattern for this file),
   also empirically executed the extracted sed/grep/cp logic against a
   synthetic throwaway tree simulating a stale pre-#255 install — confirmed
   both unit files rewrite correctly, the launch binary carries forward,
   and a second run is a true no-op (idempotency), without touching any
   real system path.

2. **(Medium, test rigor) The original test didn't check the six coupled
   paths agree with each other.** The reviewer demonstrated two broken
   fixes that stayed green against the original property-only test: nfpm
   shipping the two scripts to different directories (breaks
   `unitill-kiosk-setup.sh`'s `$(dirname "$0")` sibling lookup — full field
   outage, till never boots past a console), and the `install -D`
   destination disagreeing with `unitill-kiosk.service`'s `ExecStart`
   (cage restart-loops on tty1, black screen). **Fixed**: added two
   consistency assertions to `TestKioskHelpersStayOffPosWritableTree` —
   the two nfpm dsts must share a directory, and the `install -D`
   destination must equal the kiosk unit's `ExecStart` target. Both
   mutation-tested against the reviewer's exact two demonstrated breaks
   (correctly caught) before being trusted.

**Also fixed (non-blocking, doc-accuracy):** four comments/doc-strings
across `.goreleaser.yaml`, `unitill-kiosk-setup.sh`, `postinstall.sh`, and
`kiosk_setup_test.go` overstated "both scripts are root-executed" —
`unitill-kiosk-launch` actually runs as the kiosk user, not root (only the
unit's `ExecStartPre=+chvt` is root). Moving it is still correct (closes a
real pos→kiosk-user exposure, not a no-op), but a repo whose prior review
on this exact code was specifically about accurate security rationale
should have the durable record say what's actually true. Corrected to
distinguish the two scripts' actual privilege levels.

**Deferred, noted but not blocking:** `postremove.sh` doesn't clean up the
runtime-created `/usr/lib/unitill/unitill-kiosk-launch` leftover on package
removal (same pre-existing class as the old `/opt/unitill/bin/...` leftover
it replaces — no regression, cosmetic, fits the existing pattern
`postremove.sh` already uses for other non-package kiosk artifacts if
picked up later).

Also confirmed: no real client/shop name, no secret-shaped literal in the
diff; `unitill-desktop` (also under `/opt/unitill/bin`, unchanged) correctly
stays out of scope — it's a regular user-run desktop shell with no root
path, not an oversight; the tar.gz `archives:` section's unrelated copies of
these two scripts are untouched.

## Verdict

**Safe to merge.** Two real gaps found and fixed (upgrade-path migration;
consistency checks between coupled path literals), both TDD'd and
independently mutation-verified; four doc/comment accuracy fixes folded in;
one cosmetic item deferred. No behavior change to kiosk-staging logic
itself beyond the migration's one-time, idempotent path correction on
already-deployed boxes.
