# Code review: .deb self-update ownership (ut-docs#151)

**Date:** 2026-08-02
**Scope:** `packaging/scripts/postinstall.sh` (+12/−4),
`packaging/kiosk_setup_test.go` (new test), `internal/selfupdate/selfupdate.go`
and `internal/pages/update_api.go` (doc/error-string corrections).
**Trigger:** ut-docs#151 — retiring `deploy/raspberry-pi/install.sh` (which did
`chown -R pos:pos $DEST`) removed the only Pi provisioning route that produced
a self-updatable install. The remaining `.deb` path's `postinstall.sh` only
chowned `/opt/unitill/data`, `/var/lib/unitill`, and
`/opt/unitill/web/public/assets` to `pos` — never `/opt/unitill` itself or
`/opt/unitill/bin`. `internal/selfupdate.Supported()` requires BOTH of those
two directories writable by the running user (`unitill-pos.service` sets
`User=pos`, `WorkingDirectory=/opt/unitill`,
`ExecStart=/opt/unitill/bin/unitill-pos`), so a fresh `.deb` install — Pi
kiosk or Linux desktop alike — had NO in-app update path at all.

## Decision: option (a) of the three the ticket laid out

- **(a) chosen** — `postinstall.sh` now does an unconditional
  `chown -R pos:pos /opt/unitill` (every invocation, fresh install AND
  upgrade — a `.deb` upgrade re-extracts package files as root-owned, so it
  must reassert every time), placed before the `is_pi_appliance` kiosk-staging
  block so it applies to any `.deb` Linux install, not just Pi. This exactly
  mirrors the retired `install.sh`'s `chown -R pos:pos $DEST`, which carried
  the comment "Own the whole install tree as the pos service user. This is
  REQUIRED for self-update" — same mechanism, restored in the file that
  replaced it. No new infra, no ADR (restores an already-reviewed mechanism,
  not a new cross-cutting decision).
- **(b) apt repo + package signing** — deferred. Its own infra-scale project
  (repo hosting + signing key management), out of scope for this ticket.
- **(c) document-only, no fix** — rejected. A needless capability regression
  for Pi field support now that (a) is straightforward.

## Security trade-off, recorded explicitly (independent review finding)

Making `/opt/unitill` pos-writable is not a meaningful regression by itself —
"the service user can overwrite its own binary" is inherent to self-update
and already true of the portable tar.gz install and the macOS bundle path.
**But** the `.deb` is the only install shape that also ships a
**root-executed** helper inside that same tree:
`unitill-kiosk-firstboot.service` runs
`ExecStart=/opt/unitill/bin/unitill-kiosk-setup --auto` with no `User=` (→
root), and `.goreleaser.yaml` tells operators to `sudo` that same binary
manually. `pos` — the account running the network-facing HTTP server and
in-process third-party WASM plugins — can now plant a script that root
executes; that `pos` → `root` path did not exist before. This is inherent to
option (a) as scoped (directory write permission permits replacing any file
inside, regardless of that file's own owner — a narrower "chown just the two
required dirs" variant has the identical exposure), not something the `-R`
specifically introduces. The real mitigation — moving the root-run helper out
of the self-updatable tree (e.g. a root-owned, dpkg-managed path outside
`/opt/unitill`) — is a separate packaging change, tracked as a new backlog
card (below) rather than blocking this one.

## Verification (self, before independent review)

- TDD: wrote `TestPostinstallOwnsWholeInstallTreeForSelfUpdate` first,
  confirmed it failed against the current script with the real assertion
  error, then implemented the fix and confirmed it passed.
- Mutation-checked the test against two variants (chown reverted entirely;
  chown present but misplaced inside the fresh-install-only gate) — both
  correctly failed at the time.
- **Real ownership proof, not just script-text assertions**: created a
  genuinely unprivileged system user, laid out a `/opt/unitill`-shaped temp
  tree, and ran the actual `dirWritable()` logic from `selfupdate.go` as that
  user — confirmed `Supported()`'s two checks are `false` under the
  pre-fix ownership and `true` under the fix's ownership.
- Full `go build ./...`, `go vet ./...`, `go test ./...`: clean except one
  pre-existing, unrelated failure in `internal/issuereport`
  (`TestSaveCleansUpDirectoryOnWriteFailure`) — independently confirmed (both
  by inspection and by compiling+running the test binary as a non-root user,
  where it passes) to be a root-sandbox artifact: this container runs as
  root, which ignores the `0o500` permission bits the test relies on. Fails
  identically on unmodified `main`; unrelated package, no import edge to this
  diff.
- `bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-i18n.sh`:
  both green (no SQL, no user-facing strings touched).

## Independent review (Opus, fully independent re-verification)

Ran the full gate itself (build/vet/`go test ./packaging/...`/guards),
traced `Supported()`/`dirWritable()`/`Apply()` end-to-end against the diff,
and independently re-verified the TDD claim by reverting the fix and
re-running the test (real failure, real error message; restored, passed
again; working tree left byte-identical afterward).

**Found and fixed a real, demonstrated blocker**: the original
`TestPostinstallOwnsWholeInstallTreeForSelfUpdate` used
`strings.Index`/substring checks against the **raw script text** rather than
comment-stripped, nesting-aware code lines — reinstating a pattern this same
test file's own header already documents a *prior* independent review as
having rejected ("the first version of this test used whole-file substring
checks, and the independent review proved every fix could be reverted while
a comment kept the test green"). The reviewer demonstrated the test PASSING
against (A) the exact pre-fix broken script plus one added comment line, and
(B) a fresh-install-only-gated chown (the precise regression the test exists
to prevent) — both false passes. **Fixed**: the test now scans the script
top-to-bottom tracking shell block nesting depth (skipping comments and the
one heredoc body), requires the chown line to appear as an exact, standalone,
depth-0 (unindented, unconditional) line, and requires it positioned before
the `is_pi_appliance` gate. Re-verified against both of the reviewer's
demonstrated bypasses (now correctly fail) and the genuine fix (still
passes).

Also confirmed:
- Coverage of the deleted narrower chown lines is not lossy — both are
  strict subtrees of `/opt/unitill`; `/var/lib/unitill` (outside that tree)
  is retained separately.
- `chown -R` does not follow the `items` symlink (GNU coreutils default
  `-P`), verified empirically — no risk of chowning shop photo storage
  through the `/var/lib/unitill/items` symlink.
- No data-loss path: `Apply()`'s web-tree swap never touches
  `/opt/unitill/data` (item photos live there via `paths.Data(...)`, fixed
  2026-07-29), so a `.deb` self-update doesn't lose shop-uploaded content.
- Neither of the two recurring bug classes (missing `os.MkdirAll`;
  cwd-relative path instead of a stable one) applies — this diff has no Go
  file I/O, and all shell paths are absolute literals.
- No real client/shop name, no secret-shaped literal, anywhere in the diff.

**Non-blocking, fixed in this session anyway** (repo's own "behaviour changes
update the affected doc" rule): three doc/error strings describing the OLD
(now-incorrect) blanket ".deb → apt, never self-updates" behavior —
`selfupdate.go`'s package doc, `ErrUnsupported`'s message (user-visible via
the update API's error response), and `update_api.go`'s `registerUpdateAPI`
doc comment. All three corrected to describe the real, writability-gated
behavior.

## Deferred — new Backlog cards raised (not blockers for this ticket)

1. Move the root-executed `unitill-kiosk-setup` (and `unitill-kiosk-launch`)
   out of the now-pos-writable `/opt/unitill/bin` into a root-owned,
   dpkg-managed path outside the self-updatable tree — closes the `pos` →
   `root` path option (a) necessarily opens.
2. Retire the vestigial `web/public/assets/items` symlink dance in
   `postinstall.sh` and its stale comment (item photos moved to
   `paths.Data(...)` on 2026-07-29; the symlink handling predates that).
3. Clean `.bak` leftovers (`unitill-pos.bak`, `web.bak`) in `postremove.sh`
   now that `.deb` installs can self-update and leave them behind.
4. Root-aware `t.Skip` for `internal/issuereport`'s
   `TestSaveCleansUpDirectoryOnWriteFailure` (mirrors the pattern
   `internal/selfupdate`'s own `TestApplyReadOnlyDirIsUnsupportedAndLeavesBinary`
   already uses), so root-sandbox CI runs see a fully green suite.

## Verdict

**Safe to merge.** One real blocker found and fixed (false-passable test,
now genuinely nesting-aware and re-verified against both demonstrated
bypasses); three stale doc/error strings corrected in-session; one
deliberate, explicitly-recorded security trade-off; four items deferred to
new Backlog cards.
