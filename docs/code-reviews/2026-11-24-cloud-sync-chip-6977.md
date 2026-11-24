# Review: status-bar cloud-sync chip and "Sync now" (ut-docs#6977, slice S1)

Pre-flight: till user-visible behaviour: `web/help/en/claim.md` has a new section, "When did this till last sync? (Sync now)". `make docs-shots` was re-run after CI's `guard-docs-shots` caught the changed app surface (208 shots). Only `notices.png` (en/ar/fa/tr) changed: it now shows the chip in the status bar. `manifest.json` was refreshed. `reference/`: n/a, since no reference page describes the status-bar chips or the cloud check-in cadence (`page-navigation-map.md` lists navigation only, and the chip navigates nowhere). `README.md`: n/a, still accurate. New `en.json` keys: 10, each with `en.meta.json` metadata, English only, `review: standard`. my./admin. UI: n/a, deferred to slices S4/S5 on #6977. Host function / manifest / plan / ADR: n/a; ADR-0117 and ADR-0148 were checked and need no change. Review record: this file. Follow-ups: checklist lines on #6977 (S2–S8, plus the throttled-press feedback below).

## What shipped
- `internal/pages/cloud_sync_chip.go` (new) adds `GET /ui/cloud-sync-chip`, loaded once and then every 30 s, and `POST /api/cloud/sync-now`.
  - The chip shows "Synced just now", "Synced N min ago" (ICU plural), "Synced at HH:MM" after 60 min, or "Not synced yet".
  - It turns into the amber pill, with words, after 10 min.
  - The time comes from `registration.last_checkin_at`. Rendering the chip makes no network call.
- **Where it shows.** Only on a main till with its own cloud credentials and a paid plan (`entitlement.SyncAllowed`), and never next to the refused-credential or replaced chip. Everywhere else it renders an empty 200.
  - The plan gate keeps ADR-0148 §2 intact: an unpaid till gets no new way to open a check-in window.
- **Sync now.** Managers only (`settings` permission, else the PIN prompt).
  - It calls `requestOperatorCheckin`, then polls the key for up to 8 s with no DB transaction held. It answers "Synced just now" or "Still trying — sales keep working offline".
  - It shares the 5 s kick floor with the subscription "Check now".
- `subscription_ui.go`: the wait logic moved from `refreshEntitlementNow` into a shared `operatorCheckinAndWait`. "Check now" behaves as before, and its tests are unchanged.
- **Wiring.** Route registered in `init.go`. The path is in both poll lists in `internal/auth/middleware.go`, so polling never extends a session. In demo mode the GET is allowed and the POST denied. The mount sits in `base.html` after `#sb-cloud-auth-mount`. The CSS uses logical properties only.
- **Phone.** In the ☰ drawer only. The chip is not added to the `:has()` rule that brings the status row back onto the phone sale screen.
- **Self-order kiosk.** The customer pages don't use `base.html`, so they never get the chip.

## Review (independent Opus 5.5 reviewer; code by a Sonnet developer)
Verdict: approve with minors. No blockers or majors.
1. **Minor: fixed.** The POST asked for a manager PIN before checking whether the chip shows at all, so a crafted cashier POST on an unpaid or replica till got a prompt that led to an empty 200. The gate now runs first. New test `TestCloudSyncNow_NotShownStateNoPromptForCashier` failed before the fix and passes after.
2. **Minor: fixed.** A closed tab mid-wait logged a `context canceled` warning. It is now skipped via `errors.Is(err, context.Canceled)`.
3. **Minor: accepted, follow-up on #6977.** A press within 5 s of another check-in returns the chip unchanged with no line, even though a check-in is in flight. Showing a "Syncing…" line would need a new key, and it is one or two seconds of no feedback.
4. **Nit: accepted.** The `HX-Trigger: ut-cloud-synced` after a successful press re-fetches the chip once. It is harmless, and it is the hook for other panels later.
5. **Nit: accepted.** The two English plural forms are identical, which is valid ICU.
6. **Nit: accepted.** The stale pill copies the existing amber `#451a03` of the sibling chips rather than a token, matching them.

Checked with no finding: the refactor leaves "Check now" behaviour unchanged; the kick floor is claimed under a mutex; the channel send is non-blocking; timers are stopped; `last_checkin_at` is written on 2xx, 304 and 402; the local-time conversion; demo, unauthenticated and 405 handling; HTMX request ordering. The reviewer re-verified a TDD claim: disabling the paid-plan gate fails `TestCloudSyncChip_EmptyWhen/sync_not_allowed`, and restoring it passes.

## Tester (independent)
- **Mutation checks.** Three mutants (stale threshold, `canSync`, still-trying line) each failed their test.
- **Driven run.** Real binary with auth on, an unreachable cloud and a seeded paid entitlement.
  - At 1024x600 the four states read correctly.
  - A manager press shows "Syncing…", then "Still trying…" after about 8.1 s. A cash sale completed during the wait in 20 ms with no dialog.
  - A second press within 1 s did not kick again.
  - A cashier sees the time with no button, and a forged POST gets the PIN prompt.
  - In fa RTL the chip is mirrored with no overflow. A German-length string fits.
  - At 360x640 the chip is hidden at rest on the sale screen and sits in the drawer, with a 44 px button.
  - `/self-order` has no chip.
- **D1, medium: fixed.** The 2.25rem Sync now pill made `.statusbar` 19 px taller on every staff page at 1024x600 (49 px against 30 px). The visible pill is now border-box, 1.25rem, and `::after` (`inset-block: calc((100% - 44px) / 2)`) keeps the 44 px hit area. The stale wrapper has no block padding, and the drawer keeps a real 44 px button.
  - **After D1, re-measured by the Tester on `cd3a6c2c`.** The bar is 33.0 px with the chip against 29.7 px without (fresh and stale, `/settings` and the sale screen). The label is not clipped (button 21.25 px), the hit area is 46 px, and the drawer button is 44 px in en and fa.
  - **Accepted:** the remaining +3.3 px is less than the existing 2.25rem status-bar buttons add (`sb-release-notes-dismiss`).
  - Also re-checked: a cashier POST on a lapsed till gets an empty 200 with no PIN prompt, while on a paid till it still gets the prompt.
- **Not driven.** The success path (no reachable cloud; covered by `TestCloudSyncNow_ManagerKicksAndWaits`), AC6 (a real directive applied within 10 s), dark theme, `UT_KIOSK=1`, and real touch hardware.

## Gate
- `gofmt -l .` is clean. `go build ./...` passes. `go vet ./internal/pages/ ./internal/auth/` is clean.
- `golangci-lint run ./internal/pages/...` reports 0 issues.
- `go test ./internal/pages/ ./internal/auth/...` passes (`internal/pages` takes about 11 min on this container, without `-race`).
- All 74 guards in `ci.yml` pass locally, except four that cannot run in this container: armv7-seccomp (no qemu), catimport-version (tags not fetched), deadcode-baseline (tool built with an older Go) and shellcheck-version (no shellcheck). CI runs these.

## Verdict
Approve, with D1 and minors 1 and 2 fixed in this branch.
