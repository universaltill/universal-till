# Review — English manual: multi-till, pairing & sync (ut-docs#334)

**Date:** 2026-11-16 · **Author:** Dev (Sonnet, two rounds) · **Reviewer:** independent Opus review · **Tester:** separate agent (two rounds)

## What changed

- `web/help/en/multitill.md` (English only, ADR-0160):
  - new sections **When you need a second till**, **What is shared and what stays on each till**, **Approving a pairing request** (device, verification code, role, manager PIN, approve/deny, what the joining till sees after a deny), **Removing a till** (revoke, confirm text, audit log, register kept, how to pair again);
  - step 1 now names the **Manage tills / pair a new one** button; step 3 points to the new approval section;
  - step 4 corrected: on the Tills page the manager presses **Restart now**, only the first setup screen restarts by itself;
  - step 11 (sync chip) rewritten: tap target only for a manager, joined-till tooltip texts, main-till counts, dot only for quarantine or waiting pairing requests, quarantine list name and second entry point;
  - status-bar update labels now quote the real `Updating to v… soon` / `Update to v… needed`.
- `web/help/img/manifest.json`: `multitill.en` topic hash refreshed (sha256 of the topic file, as `guard-docs-shots.sh` computes it).

## Review

Every new or changed claim checked against code, not the notes:

- Pairing approve/deny: `internal/pages/pairing_api.go` (approve takes `role`, both approve and deny need the manager PIN via `authorizePairingManager`; deny deletes the row and audits). Deny → joining till: `pairing_join.go` `pairStatusHandler` treats the 404 as still waiting until `pairingRequestTTL` (10 min, `pairing_api.go:31`), then renders `tills.pairing.no_response`. Matches the round-2 text.
- Restart: `pairWaitViewPolling` sets `autoRestart` only for `/api/setup/pair-status`; paste-a-code `renderJoinSuccess(..., false)` from the Tills page, `true` from setup (`sync_api.go:1072/1113`). Matches step 4.
- Revoke: `sync_api.go` revoke handler — `sync_management` gate, refused on a joined till, `DeleteTill` only (register untouched), `Link.Disconnect`, `till_revoked` audit row; button only where `not $.SyncPrimary` (`tills_roster.html`), with `hx-confirm` text `tills.revoke_confirm`.
- Sync chip: `web/ui/partials/sync_chip.html` + `/ui/sync-chip` (`sync_admin.go`) — `canManage` link vs span, replica dot `or .offline .queued .rejoin`, primary dot `or .quarantined .pending`, stale tills as text count, quarantine href `/sync-quarantine`.
- Shared vs per-till: `internal/data/sync_admin_repo.go` `adminTables` (catalog, users/roles, customers, registers, stock locations, settings, …); vouchers via the main till (`voucher_sync_proxy.go`).
- Every bold label in the diff exists verbatim in `web/locales/en.json` (scripted check; the two `v…` labels are `sync.link.update_following`/`update_manual` with `%s` shown as `…`).
- Plain shop-owner language, no route names or code terms; no compliance claims or competitor names; RTL arrow rule n/a (English).

Findings:

- **F1 low — fixed.** Approval step 2 said "press **Deny**" before the PIN step, but Deny also needs the manager PIN (same `authorizePairingManager` gate); an owner following the steps in order would get "Incorrect manager PIN.". Now: "don't approve … Deny it instead (steps 4 and 5)".
- **F2 low — fixed.** "Its own status bar may still show it as connected **for a while**" implied the revoked till corrects itself; the tester saw "Connected to the main till" / "Main till: polling" persist, and nothing in `deriveLinkView` turns a revoked link into an unreachable or rejoin state. Now: "Its own screen may keep showing it as connected (for example **Main till: polling**)".
- **F3 nit — fixed.** The shared list omitted customers, which sync from the main till (`adminTables` `customers`). Added.
- Round-1 tester failures (Deny sentence, step 4 restart, revoked-till status) confirmed fixed in round 2.

## Extra verification

- Guards, all exit 0: `guard-help-topics`, `guard-help-drift` (only expected non-blocking drift warnings for translated `multitill`), `guard-i18n`, `guard-compliance-claims`, `guard-competitor-naming`, `guard-help-breadcrumbs`, `guard-help-terms`, `guard-mail-addresses`, `run-docs-shots-guard` ("33 routed topics × 1 locales screenshotted and fresh"; re-run after the reviewer's edits and hash refresh). `go test -count=1 ./internal/manual/...` ok. `gofmt -l .` empty.
- Tester (round 2) drove two real tills (same build, `:8191` main, `:8192` joining, fresh data dirs, Playwright Chromium 1280×800): Show pairing code, Request to pair, matching verification code, pending row + chip dot, role select, Deny (row and dot clear; joiner keeps waiting), Approve → **Restart now** (no auto-restart on the Tills page), joined rosters on both tills, Revoke confirm dialog and row removal, revoked till keeps selling. Visual attestation at 1280×800 light, en: shots 334-01 … 334-08, 334-20, 334-21, 334-23, 334-24 (scratchpad). Not driven: wrong-PIN text (auth off; template-verified), non-manager chip, quarantine, real 10-min expiry, real mDNS.

## Verdict

**Approve** with the three small fixes above applied.

## Deferred

- Inline screenshot of the pending-pairing row: deferred — its verification code is derived per request and is not deterministic, so `docs-shots-determinism` would fail. Needs a seeded/fixed-code harness first (Backlog card).
- Product gap (Backlog card, not fixed here): a revoked till keeps showing **Connected to the main till** on its sync chip and **Main till: polling** in the status bar, and still lists itself in its roster, although its log says the main till no longer accepts its pairing. It should show the **Join the main till again** (rejoin) state.

Pre-flight: review record → this file; reference pages → n/a (no behaviour change, manual-only); help topic → `web/help/en/multitill.md` + manifest hash (this change); en.json → n/a (no new keys); ADR → n/a; README → not stale; follow-ups → two Backlog cards above (pending-pairing screenshot, revoked-till status).
