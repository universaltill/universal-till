# Review — Users and Shifts swap the changed region instead of reloading (ut-docs#2762, part)

Date: 2026-09-25 · Branch: `fix/2762-users-shifts-targeted-swap` ·
Author: Opus 5.5 (pipeline, `lane:cloud-24`) · Reviewer: Fable (independent subagent, one round)

## What shipped

The product owner asked for screens to update in place instead of posting and
reloading (ut-docs#2762). This is the first slice: `/users`, `/shifts` and the
manager-PIN elevation dialog's retry. The other screens in the card stay open on
ut-docs#2762.

- **`UT.refreshRegion(el)`** (`web/public/app.js`) is a shared helper. It finds
  the nearest `[data-ut-refresh="<selector>"]` ancestor of `el` and re-fetches
  the current URL as a plain, uncached page GET. It swaps just that element
  and runs `htmx.process` on it. The triggering action's own message (`el`, or
  its `hx-target`) is carried over. If anything goes wrong (no host, a
  non-2xx, the region missing from the response, a network error), it falls
  back to the old full reload.
- **`users.html`:**
  - The list is `#users-list`.
  - The pin, active and role row forms, and the create form, refresh it when
    `X-UT-Response: ok` comes back.
  - The create form also clears itself.
- **`elevation_prompt.html`:** on `ok`, the dialog refreshes around its own
  `hx-target`. It also clears a `form[data-ut-reset-on-ok]` ancestor. Sites
  with no `data-ut-refresh` ancestor (settings) still reload, as before.
- **`shifts.html`:**
  - Opening or closing a shift swaps `#shifts-page`, which covers the status
    tag, the current card, the history and the form.
  - The denomination-grid `change` listener is now delegated from `body`,
    because the grid now arrives by swap. It is still dropped on boosted
    navigation away (`base.html` tracks page-script body listeners).

## Review findings (Fable)

Nothing blocking. The reviewer verified these are non-issues:
- ADR-0098 listener tracking.
- Swap ordering: no `globalViewTransitions`, so the message is in place
  before `afterRequest`.
- `htmx.process` re-applying `hx-boost="false"` and `hx-on`.
- OSK and autofill MutationObservers.
- UUID user IDs as selectors.
- The settings-site fallback.

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | Medium | Every non-empty aria-live message in the region was carried over. A stale elevation hint or refusal in another row would survive a refresh, where the reload used to clear it. | **Fixed.** Only the triggering message is kept. The e2e test plants a stale message in another row and asserts it is gone. |
| 2 | Medium | The elevation-dialog → refresh path has no browser test. | **Not fixed here.** On a stock till, anyone who can open `/users` already holds `user_management`, so the dialog can't be reached without customising the permission matrix. It is covered by the Go guard (the handler calls `UT.refreshRegion`) and falls back to a reload. The gap is recorded on ut-docs#2762. The users e2e test ran green on the default project, where it asserted "Saved.", not the dialog. |
| 3 | Low | Focus drops to `<body>` after the swap. | Left as is. A reload did the same. |
| 4 | Low | A second tap while the refresh is in flight loses that tap's confirmation. | Left as is. The state still converges. |
| 5 | Low | A throw after `replaceWith` double-renders through the reload fallback. | Accepted. The fallback is correct. |
| 6 | Nit | The `errKey` banner sits outside the region. | Left as is. Only the untouched plain-POST promote path sets it. |

## Verification

- **TDD, Go guard:** `TestUsersShiftsSwapRegionNotReload` fails on the old
  templates (reload calls, no region) and passes now.
- **TDD, e2e:** `e2e/tests/targeted-swap-users-shifts-2762.spec.ts` fails on
  the old templates (2 failed) and passes now. It sets a `window` marker
  first, so a real reload would fail the test. It covers:
  - `/users`: create, then deactivate and re-activate (so the swapped forms
    are live again). The confirmation survives and a stale message does not.
  - `/shifts`: open, then denomination count to `count_protocol` on the
    swapped grid, then close.
- **Neighbouring specs green:** `htmx-senderror-refund-shifts-1707`,
  `shifts-tips-osk-1272`, `fiscal-register-address-form-overflow-2420`
  (its `/users` forms check).
- **Pixels:** full-page screenshots of `/users` and `/shifts` before and after
  are byte-identical. That is why only the docs-shots surface hash was
  refreshed (`Docs-Shots-Unchanged`).
- **Gate:**
  - `gofmt`, `go build ./...` and `go test ./...` pass.
  - `golangci-lint` reports 0 issues.
  - The CI guards pass, apart from three that fail for reasons unrelated to
    this change:
    - `guard-deadcode-baseline_test` fails on clean `main` too.
    - `shellcheck` is not installed in this container.
    - `retry-with-backoff.sh` is a helper, not a guard.
