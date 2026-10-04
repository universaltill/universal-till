# Code review — pairing, revoke and backup refresh their card, not the page (ut-docs#2904)

Date: 2026-10-03 · Lane: `lane:cloud-54` · Branch: `feat/2904-hx-refresh-fragments`
Author model: Opus 5.5 · Reviewer: Fable (independent subagent, own worktree)

## What shipped

Four handlers answered with `HX-Refresh: true`, so htmx reloaded the whole
page after each action. They now follow the #2762 targeted-swap pattern
(`data-ut-refresh` region + `data-after-request` → `UT.refreshRegion`):

| Action | Handler | Refreshes |
|---|---|---|
| Approve / Deny pairing | `pairing_api.go` | `#tills-pairing-card` (`refresh-region:tills-pairing-card`) |
| Revoke till | `sync_api.go` | `#tills-roster` (`refresh-region:tills-roster`) |
| Backup now | `backup_api.go` (now `X-UT-Response: ok`) | `#settings-backup-region` (`ut-ok refresh-region`; elevated retry via `elevation-done`) |

- Approve/deny/revoke also send `HX-Trigger: tills-changed`; the nav
  `#sync-chip` and `#pairing-notice-mount` (30 s polls) listen for
  `tills-changed from:body`, so the pending-pairing dot and notice still
  clear at once, as the reload used to do (and as `web/help/en/multitill.md`
  step 11 promises).
- `inline-actions.js` `refresh-region` takes an optional region id
  (`refresh-region:<id>`), so a carrier a poll swapped out mid-request
  still finds its live region instead of falling back to a full reload.
- The JSON contract of approve/deny (#184) is unchanged.
- docs-shots surface hash refreshed (attributes, a wrapper `<div>` with no
  class and comments only — no CSS child selectors touch it; no pixel changed).

## Review findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | `data-ut-refresh` on the `#settings-backup` card itself: the settings section switcher holds the card element, so after a swap it toggled a detached node and the Backups card stayed visible under every other section (reproduced in a browser). | **Fixed** — region moved to an inner `#settings-backup-region`, as every `settings-*-region` does. Go test pins it; e2e switches section after Backup now and asserts the card hides. |
| 2 | major | Refreshing the whole card re-imported `backup_restore_staged.html`, whose Restart button is wired by an inline script a region swap never re-runs → after "Back up now" with a staged restore, Restart would re-exec the till with no "restarting…" / redirect. | **Fixed** — `#restore-msg` sits outside the region; Go test pins region < `#restore-msg` and that the region closes before it. |
| 3 | minor | Nav pairing notice stale up to 30 s after approve/deny. | **Fixed** (found by author in parallel) — `HX-Trigger: tills-changed`, covers the sync-chip dot too and revoke. |
| 4 | minor | Revoke vs the roster's 10 s poll: on browsers where a tapped button takes no focus (Safari/iOS) the poll can detach the button mid-request → `closest()` fails → full-reload fallback. | **Fixed** — `refresh-region:<id>`. |
| 5 | nit | Same race for approve/deny vs `#pending-pairings`' own 30 s poll. | **Fixed** by the same change. |
| 6 | nit | Pairing card refresh is two round-trips (page GET + lazy partial GET). | Accepted — consistent with the #2762 pattern. |

Design checks the reviewer confirmed: approve does not change the roster
(the `tills` row appears only when the replica calls `/api/sync/enroll`);
the backup elevation path (`elevation-prompt` → no refresh; approved retry →
`X-UT-Response: ok` → `elevation-done` refreshes and keeps the ✓); 403/500
paths set no header and don't refresh; a 204 counts as `ok` in htmx 1.9.12;
no other client depends on these `HX-Refresh` headers.

## Verification

- TDD: tests flipped first and seen failing with the real error (HX-Refresh
  "true", missing anchors / HX-Trigger); the reviewer independently
  reverse-applied the source changes and confirmed the same failures, then
  passes on restore.
- `go build ./...`, `go vet`, `go test ./... -race` (every package passed
  except `internal/{db,pages,plugins}`, which hit Go's 10-min default under
  `-race` on this 4-core container — CI doesn't race them either; re-run as
  CI does: `internal/pages` and `internal/db` with a longer timeout,
  `internal/plugins` with `-timeout 20m`, all pass). The full run caught four
  older tests pinning the exact attribute strings changed here
  (`fail unhide:`, the notice mount's `hx-trigger`, `UT.refreshRegion(ctx.el)`)
  — anchors updated, intent unchanged. Every `build`-job guard in `ci.yml`
  passes (shellcheck not installed in this container; no `.sh` changed).
- Playwright, real till: `targeted-swap-tills-backup-2904.spec.ts` (new: a
  `window` marker survives deny, approve, revoke and Backup now; regions
  really replaced; ✓ kept; new snapshot listed; section switch still hides
  the card) plus the neighbouring `targeted-swap-*` and
  `tills-pairing-layout-1548` specs — 11/11 pass.
- Visual: no screenshots looked at for this change. Layout is unchanged by
  construction (no CSS, no new strings), and the 1024×600 / 1280×800
  overflow measurements in `tills-pairing-layout-1548` still pass. Not
  checked on real touch hardware.

## Verdict

Safe to merge.
