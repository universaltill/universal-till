# 2026-09-12 — POST /api/settings/upsert doesn't publish self-order mode or revoke the session (ut-docs#2121)

## What shipped

`POST /api/settings/upsert` — the generic key/value settings editor
(`internal/pages/settings_page.go`) — accepted `key=display.mode` and
persisted arbitrary values with no side effects, unlike the dedicated
`POST /api/settings/display-mode` handler in the same file. Two gaps:

1. Setting `display.mode=self_order` through the generic editor did not
   call `httpx.InitSelfOrderMode(true)`, so `record_dialog.html`'s
   "selforder" template flag (status/lock/exit-to-OS withholding,
   `coding-standards.md` §10, ut-docs#2099) stayed stale until the process
   restarted — same class of bug ut-docs#2099's review found and fixed in
   `newRederiveSettings` (the cloud `set_setting`/replica-drift path), just
   reached through a different generic-editor door.
2. It also skipped ut-docs#1259's session-revoke-on-self_order-entry
   protection: the acting session must not survive a switch into
   self-order mode, since that surface is customer-facing and auth-exempt
   (`/self-order`, `/api/self-order/*`). This gap pre-dates #2099 — it's
   the same "generic key/value door doesn't get the dedicated handler's
   side effects" shape, just a second instance of it.

Fix: added a `case "display.mode":` to the upsert handler's post-write
switch, mirroring the dedicated handler's own effects exactly —
`httpx.InitSelfOrderMode(value == "self_order")`, and (only when entering
self_order) revoking the acting session, clearing its cookie, and writing
the same `self_order_session_revoked` audit entry the dedicated handler
writes.

Two new regression tests in
`internal/pages/settings_upsert_display_mode_test.go`:
- `TestSettingsUpsertDisplayMode_UpdatesLiveKioskFlag` — drives the switch
  through the upsert endpoint and asserts the record dialog's affordance
  disappears/reappears live, same assertion shape as
  `categories_page_test.go`'s existing test for the dedicated handler.
- `TestSettingsUpsertDisplayMode_RevokesActingSessionOnSelfOrderEntry` —
  mirrors `TestSelfOrderMode_RevokesActingSessionOnEntry`, but through the
  upsert endpoint: asserts the cookie is cleared, the old token no longer
  resolves server-side, and the revoke is audited.

## Independent review

Reviewed by a fresh Opus subagent (complexity:medium → Sonnet builds,
Opus reviews) in an isolated git worktree, with its own revert-then-restore
TDD re-verification (not just re-run of the tests as committed):

- Removed exactly the new `case "display.mode":` block, re-ran both new
  tests — both failed with the claimed symptoms (dialog still renders the
  lock affordance immediately after the switch; no cookie is cleared at
  all, i.e. no revoke).
- Restored the fix — both passed again.
- Confirmed neither test can pass vacuously: test 1's helper fails on any
  non-2xx, test 2 asserts three independent properties (cookie `MaxAge <
  0`, the old token no longer resolving, and exactly one
  `self_order_session_revoked` audit row).

**Verdict: safe to merge**, no blocking findings. Verified independently:
`go build ./...`, `go vet ./...`, `gofmt -l .`, `golangci-lint run
./internal/pages/...` (0 issues), `guard-data-access.sh`,
`guard-kiosk-engine.sh`, `guard-i18n.sh` all green; full
`go test ./internal/pages/...` green.

Findings raised, all NOTE-level (no BLOCKING):

1. **Fixed in this same session** — `TestSettingsUpsertDisplayMode_UpdatesLiveKioskFlag`'s
   baseline assertion relied on `httpx`'s process-global self-order flag
   already being `false` from some earlier test's cleanup, rather than
   setting it explicitly first. A narrow `-run` filter that skipped the
   test that happens to reset it could produce a spurious failure. Fixed
   by calling `httpx.InitSelfOrderMode(false)` explicitly before the
   baseline check, matching `categories_page_test.go`'s own precedent.
2. **Fixed in this same session** — the session-revoke test entered
   self_order mode and never reset the global flag afterward, leaving it
   `true` for whatever test runs next in the package. Added
   `t.Cleanup(func() { httpx.InitSelfOrderMode(false) })`.
3. **Accepted, not changed** — this path audits the generic
   `setting_upserted` action (existing convention for every key in this
   handler), not the dedicated handler's `display_mode_changed`. Changing
   it would create the "two rows, one write" ambiguity the fiscal-toggle
   audit's own comment in this file already warns against; the
   security-relevant event (`self_order_session_revoked`) is written
   either way.
4. **Accepted, not changed** — the generic editor doesn't validate `value`
   against `register|backoffice|self_order` the way the dedicated handler
   does, so it can persist a value the dedicated handler never would (e.g.
   a literal `"register"` instead of `""`, or garbage). Checked every
   reader of `display.mode` (`index_page.go`, `init.go`, `update_api.go`,
   `cloudsync.go`, `settings.html`'s template): all of them treat anything
   that isn't exactly `"self_order"`/`"backoffice"` as register, and the
   new `InitSelfOrderMode(value == "self_order")` call uses the identical
   predicate `init.go`'s own boot-time init uses — so the live flag can
   never diverge from what any reader takes the persisted value to mean.
   Harmless by construction; not the scope of this card.
5. **Accepted, not changed** — entering self_order via this raw-table card
   has no client-side redirect to `/` the way the dedicated handler's form
   does, so the acting browser is left on a stale, already-rendered
   `/settings` page after its cookie is cleared. The security property
   holds regardless (every subsequent request is anonymous, `/` 303s to
   `/self-order`), and this card is manager-only power-user tooling with
   an explicit existing comment against elevation-wiring it further. Out
   of scope for a security-focused card; noted for a future UX pass if
   this raw editor's self-order affordance ever gets its own dedicated
   control.

## Verified beyond automated tests

- Manually traced every consumer of the `"display.mode"` setting
  (`index_page.go`, `init.go`, `update_api.go`, `cloudsync.go`,
  `settings.html`) to confirm the missing validation (finding 4 above)
  cannot produce a state any of them mishandles.
- Confirmed via `git show --name-only` that the diff touches only
  `internal/pages/settings_page.go` and the new test file — no
  `web/ui/**` or `web/help/**` change, so the UX-guidelines checklist and
  the user-manual-update requirement genuinely don't apply (verified, not
  assumed).
- Confirmed the new case sits after the `d.Settings.Set` error-return, so
  the live flag/revoke can never fire on a failed write.

## Deferred / out of scope

- Auditing this path's `display.mode` writes under the dedicated
  handler's `display_mode_changed` action name (finding 3) — a
  deliberate convention choice, not a gap; left as-is.
- Validating `value` against the closed `register|backoffice|self_order`
  enum on this generic path (finding 4) — provably harmless today; would
  be a larger, separate change to add real rejecting validation to a
  raw-table editor that also handles many other keys with no such
  validation.
- A client-side redirect after entering self_order via this card
  (finding 5) — UX polish, not a security gap; not filed as a new card
  since it's speculative until someone actually hits it in practice.
