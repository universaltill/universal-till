# Code review — self-update on Pi kiosk installs (board #147, #148)

- **Date**: 2026-07-31
- **Branch**: `fix/selfupdate-kiosk-writability` → PR #111
- **Cards**: ut-docs #147 (p1 field report — Settings→Update dead-ends at the
  website on the Pi kiosk) and #148 (asset-staleness follow-on, found during
  the review and folded into this PR).

## The bug (#147)

On the Raspberry Pi kiosk the in-app updater refused to run, so Settings →
Update rendered a link to the website download page — a dead end on a
fullscreen kiosk with no way out. Two coupled causes:

1. `supportedFor()` blanket-blocklisted any `/opt/unitill/*` path as
   "packaged → apt". But the Pi installs a *portable* binary there
   (`deploy/raspberry-pi/install.sh`), owned by the `pos` service user — it
   self-updates fine. The path prefix was a wrong proxy for the real question.
2. Nothing checked the *real* precondition for the in-place swap (an
   `os.Rename` within a directory): that the directory is **writable by the
   running service user**.

## The fix

- `supportedFor(exe, goos)` is now a pure OS/location policy gate: windows and
  android/ios never; darwin always (the `.dmg` bundle path); `/usr` stays
  apt's domain; everything else — including `/opt/unitill` — is
  location-eligible.
- `Supported()` applies the true precondition via `dirWritable()` (a
  create+remove temp-file probe) on **both** the binary's directory and the
  server's working directory (see #148 below).
- The update-check fallback (`internal/pages/update_api.go`) no longer renders
  a website link on non-Windows installs: Windows keeps the actionable
  download link; unix shows a plain "in-app update isn't available for this
  install" message (new i18n key `settings.update.unavailable_here`, all 4
  locales). A correctly provisioned kiosk never reaches this branch —
  `Supported()` is true, so the inline Apply button shows instead.
- `install.sh`: documented that the `chown -R pos:pos` is REQUIRED for
  self-update (not cosmetic), so a later in-place rebuild doesn't silently
  re-break it.

## Independent (opus) review — found a BLOCKER, fixed in the same PR

The reviewer confirmed the OS/location-policy split and writability concept
are sound, and **verified the `.deb` non-regression**: the `.deb` runs as
`pos` and `postinstall.sh` leaves `/opt/unitill/bin` root-owned, so
`dirWritable` is false there → still defers to apt. Correct.

**BLOCKER (fixed): web/ was swapped at the wrong directory on the exact kiosk
layout this change enables (#148).** `Apply` computed the web-swap target from
the binary's directory (`filepath.Dir(exe)/web`). On the Pi the binary is at
`/opt/unitill/bin/unitill-pos` but the server reads its on-disk `web/` override
**cwd-relative** (`WorkingDirectory=/opt/unitill` → `/opt/unitill/web`), and
`fallbackFS.Open` serves the disk side *ahead of* the embedded side. So a
self-update would swap the binary (templates + locales update from the embedded
FS, fine) but dump the new `web/` into `/opt/unitill/bin/web` where nothing
reads it — leaving **stale `/public` CSS/JS/themes** shadowing the new binary's
embedded assets, while reporting success. No existing test caught it because
every `Apply` fixture used a flat layout.

Fix: `Apply` now resolves the web base from the working directory (`os.Getwd`),
matching how the server reads it — unchanged for a flat install (cwd ==
installDir), correct for the bin-subdir kiosk layout. `Supported()` was
extended to also require the working directory writable (the SHOULD-FIX the
reviewer flagged — otherwise a partially-owned tree would pass the gate then
fail mid-swap). New `TestApplySwapsWebAtWorkingDirNotBinaryDir` builds the
split layout and asserts web is swapped at `cwd/web` and never at `bin/web`;
**mutation-probed** (reverting the web base to the binary dir fails it on both
assertions — stale web and a wrongly-created `bin/web`).

**Accepted nits (not changed):** `ErrUnsupported`'s message ("use the
installer / apt") is slightly off for a merely root-owned-but-portable install,
but the UI gates on `Supported()` and shows the clean fallback, so it only
surfaces in logs — low value. `dirWritable`'s probe file could linger if the
remove fails after a successful create (rare; `O_EXCL` + random name, same-user
local dir — no security concern). The `.deb`-stays-unsupported path and the
"exists but not writable" branch are only covered indirectly in root-run CI
containers (the read-only-dir test skips under root); the split-layout test now
covers the layout that actually mattered.

## Verification

- `go build ./...`, `go vet`, full `go test ./...`, `guard-data-access.sh`,
  `guard-i18n.sh` — all green.
- selfupdate unit tests (policy split, writability gate, split-layout web
  swap, macOS bundle paths — no regression); `update_api` fallback link/no-link.
- **Real Pi4 end-to-end**: see the close-out comment on #147 — deployed the
  fixed arm64 build to a service-writable `/opt/unitill`, confirmed the Update
  button now appears (not the website link), and drove a live self-update +
  re-exec with the kiosk reconnecting.

## Note

The first fixed build must be placed on the Pi manually — the running v0.2.50
binary can't self-update to the fix (that's the bug). Every subsequent update
is in-app. `install.sh`'s pos-ownership is what keeps it self-updatable.
