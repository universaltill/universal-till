# Code review — till-registration feedback fix + uninstall command

Date: 2026-07-16
Branch: `fix/enrol-feedback-and-uninstall`

## What changed & why

### 1. "Register now" button did nothing (bug fix)
Reported: the Settings → Till registration "Register now" button appeared dead.

Root cause: `POST /api/enrol/now` returned **HTTP 502** on a failed
registration and **403** when not a manager. The button uses
`hx-post … hx-swap="innerHTML"`, and HTMX **silently drops non-2xx responses**
(no swap), so the `#enrol-msg` span never updated — the click looked like a
no-op. Because the default marketplace endpoint is `http://127.0.0.1:8081`
(a dev default), a real deployment's registration *always* fails, so the
button *always* looked dead.

Fix (`internal/pages/settings_page.go`): the handler now **always answers 200**
with an HTML message HTMX will swap in:
- success → `✅ <registered> — <store id>`
- failure → `❌ <failed>: <concrete reason> (<endpoint we tried>)`, surfacing
  the real error (e.g. connection refused) and the endpoint, so a
  misconfigured/unreachable marketplace is diagnosable from the UI.
- not-a-manager → a translated "manager or admin only" message (was a dropped 403).

New i18n key `settings.enrol.forbidden` added to **all** locales (en/ar/fa/tr).

### 2. Uninstall command (feature)
Portable builds had no removal path. Added double-click/CLI uninstallers that
stop a running till, then **ask before deleting shop data** (DB, plugins,
backups — never silent), and point the user to delete the app folder last:
- `packaging/macos/uninstall-unitill.command`
- `packaging/linux/uninstall-unitill.sh`
- `packaging/windows/uninstall-unitill.bat`
- `.dmg`: `make-dmg.sh` now stages an **"Uninstall Universal Till.command"**
  that also removes `/Applications/Universal Till.app`.
- `.deb`: new `postremove.sh` deletes `/var/lib/unitill` + `/opt/unitill/data`
  **only on `purge`** (`apt remove --purge`); a plain `apt remove` keeps data.

All shipped via goreleaser archives + nfpm scripts; `goreleaser check` passes.
Each uninstaller uses the correct per-OS data dir from `internal/paths`.

## Risk / correctness
- Handler change is display-only; still manager-gated (message, not action).
- Data deletion is confirmation-gated everywhere; `.deb` purge matches dpkg
  convention. Uninstallers can't delete their own running folder — documented.

## Checks
`go build ./...`, `go test ./...`, data-access guard, i18n guard,
`goreleaser check` — all pass.

## Not addressed here (follow-up)
The deeper model note from the user ("register the store first, then till
devices") — making marketplace enrolment two-tier (one store/merchant org,
many till devices, per-store entitlements) instead of one-store-per-till — is a
cross-repo ADR-0013 change tracked separately; this PR only makes the existing
button work and reportable.
