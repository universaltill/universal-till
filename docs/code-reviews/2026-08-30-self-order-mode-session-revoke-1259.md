# Code review — self-order mode must revoke its own acting session (ut-docs#1259)

- **Date:** 2026-08-30
- **Branch:** `fix/1259-self-order-mode-revokes-session`
- **Reviewer:** two rounds, independent Opus subagents in isolated
  `worktree`s (this pipeline's `complexity:medium` review tier) — a
  second round was earned by the first finding a blocker (B1).
- **Verdict: SAFE TO MERGE.** One blocking finding (B1), fixed, then
  re-verified in a second scoped round. Four should-fix findings
  (S1-S4), all fixed. One mechanical CI blocker surfaced by the second
  round (stale docs-shots manifest), fixed. Everything below reflects
  the final, fixed state.

## What shipped

Follow-up from the ut-docs#1253 fix
(`docs/code-reviews/2026-08-28-self-order-exit-pin-bypass.md`), finding 1.
`POST /api/settings/display-mode` (`internal/pages/settings_page.go`)
persisted `display.mode` and nothing else. #1253 closed every path
reachable by *tapping through* the kiosk UI, but the acting browser's own
session — often a manager/admin, since the action is
`canPerform("settings")`-gated — kept sitting in the till's browser after
the switch. On a till where the OS chrome/URL bar is reachable (a desktop
till put into self-order mode, unlike a locked-down Pi kiosk appliance),
a customer could type `/settings` directly and reach it with that stale
session — `checkOrElevate` returns `allowed` outright once `canPerform`
passes for a live session, so zero-PIN.

- `internal/pages/settings_page.go` — `POST /api/settings/display-mode`,
  when `mode == "self_order"`: resolves and audits the acting session
  (`session_revoked_self_order`, same shape as the existing `logout`
  audit), revokes it via `d.AuthSvc.Logout`, clears the cookie, and sends
  `HX-Redirect: /self-order` so the browser navigates immediately —
  including the "elevated" (override-PIN) success path, whose
  `text/html` response deliberately doesn't self-navigate (that guard is
  for a genuine elevation *prompt*, a different, earlier return).
  `register`/`backoffice` are unaffected — the device is still staffed.
- `internal/pages/auth_page.go` — `GET /login`: when `next != "kiosk"`
  and there's no live session, a till with `display.mode == "self_order"`
  now redirects to `/self-order` instead of rendering the PIN keypad —
  same landing a logged-in visitor already gets via `registerIndex`'s
  `/` redirect, extended to cover having no session at all. `next=="kiosk"`
  (the PIN-gated kiosk exit link, #1253) is untouched — excluded from
  this branch exactly as it's excluded from the existing shortcut above it.
- `internal/pages/self_order_mode_test.go` — new
  `TestSelfOrderMode_RevokesActingSessionOnEntry`: full round trip
  against a real `auth.Service`/DB (manager logs in for real, switches to
  self-order, asserts the cookie is cleared, the token is dead
  server-side, the audit row exists, the `HX-Redirect` header is present,
  an anonymous `GET /` and `GET /login` both land on `/self-order` rather
  than a stranded keypad, `?next=kiosk` still always demands a PIN, and
  switching back to `register` doesn't touch a fresh session). Two
  pre-existing test comments that asserted "self-order mode never logs
  the till out" (now false) were corrected in the same branch.
- `web/help/en/self-order.md` — one sentence: switching into self-order
  signs the acting browser out, and how to get back in (the lock icon +
  PIN).
- `web/help/img/manifest.json` (+ 2 regenerated screenshots) — `make
  docs-shots` re-run; `guard-docs-shots.sh` hashes whole files for any
  route-registering file, and both changed `.go` files register
  screenshotted routes.

## Independent review

### Round 1 — blocker found

**B1 (blocking):** the original fix stranded the till. Its own launch URL
is `/` (`cmd/unitill-desktop/desktop.go`), which isn't auth-exempt; once
the acting session was revoked, `/` → 303 `/login`, and `/login` had no
affordance back to `/self-order` for an anonymous visitor (only
`?next=kiosk` renders the "back to self-order" link). Verified live:
`POST display-mode(self_order) → 204`, then `GET / (no cookie) → 303
/login`, then `GET /login → 200` with no `/self-order` link anywhere in
the body. The only way back in was logging in again — recreating the
exact live session this ticket exists to revoke. Net effect as originally
shipped: protects only the window between the switch and the next login,
at the cost of breaking the kiosk display outright.

Should-fix alongside it: **S1** the "elevated" success path left the
operator on a dead `/settings` page ("✓ approved") over an already-revoked
session with no explanation; **S2** two comments now asserted something
false; **S3** no manual update for a change to what a shop owner
experiences; **S4** the revoke wrote no audit trail, unlike `logout`.

### Fix

- `GET /login`'s new self-order branch (above) closes B1 directly.
- `HX-Redirect: /self-order` on the handler's self-order branch closes
  S1 for the elevated path (round 2 confirmed the 204 fast path still
  gets there via the existing client JS + the new `/login` redirect —
  a couple of extra round trips, not a dead end; noted below as N2, not
  worth the added complexity to special-case).
- S2/S3/S4 fixed as described in "What shipped" above.

### Round 2 — scoped re-verification

Re-reviewed only the delta since round 1 (not the whole diff again), plus
mutation testing on each new assertion:

- **B1: fixed.** Traced the full chain against real code (till launch URL,
  middleware exemption list, the new branch, the `?next=kiosk` guard) and
  confirmed each hop; probed `?next=kiosk` directly (still 200 + PIN form,
  not widened). Mutation: removing just the new `/login` branch makes
  `TestSelfOrderMode_RevokesActingSessionOnEntry` fail on exactly the new
  landing assertion; restoring returns it to green.
- **S1-S4: addressed**, each independently probed (elevated path headers/
  cookie/audit, register/backoffice/unset-mode non-interference, exit-to-OS
  escape hatch still reachable on `?next=kiosk` so nobody can be locked
  out). Two cosmetic nits raised (manual said "Display → mode" instead of
  the control's real label "Device profile"; a test comment's "logging in
  normally" phrasing had gone slightly stale) — both fixed.
- **New finding this round (mechanical, blocking):** `guard-docs-shots.sh`
  failed — both changed `.go` files register screenshotted routes, so
  their whole-file hash changed and invalidated the manifest, even though
  no screen's pixels actually changed. Fixed by running `make docs-shots`
  and committing the regenerated manifest.
- **N2 (non-blocking, accepted as-is):** `HX-Redirect` is inert on the
  plain 204 path specifically — the vendored htmx's own response handling
  runs before `settings.html`'s `hx-on::after-request` handler, which then
  overwrites `location.href` with `/` for any non-`text/html` response
  regardless of `HX-Redirect`. Net effect there is `/` → `/login` →
  `/self-order`: right screen, two extra redirects, no dead end. Left
  as-is — the header is genuinely load-bearing on the elevated path (what
  S1 asked for), and special-casing the inline JS to prefer `HX-Redirect`
  adds complexity for a cosmetic extra hop on an already-working path.
- **Informational, verified safe, no change needed:** the new `/login`
  branch sits before the first-boot → `/setup` redirect, but a
  first-boot till has neither operators nor a `display.mode` row, so this
  is unreachable in practice. A cloud `set_setting display.mode=self_order`
  directive bypasses the POST handler entirely (no revoke on that path) —
  genuinely out of scope for this ticket; the new `/login` landing still
  behaves correctly regardless of how the mode got set.

TDD claims independently re-verified both rounds, not taken on the
implementer's word: revert-then-restore on the original fix (B1's
symptom reproduces, cookie/resolve assertions fail with the exact
expected messages), and mutation testing on each of the round-2 additions
(`/login` branch, `HX-Redirect` line, audit-before-Logout ordering) —
each mutation fails a different, specific assertion, then restoring
returns the suite to green.

## Verification

| Check | Result |
|---|---|
| `gofmt -l .` | empty |
| `go build ./...` / `go vet ./...` | clean |
| `go test ./internal/pages/... -run 'TestSelfOrder\|TestDisplayMode\|TestLogin\|TestLogout\|TestBackoffice' -race -count=1` | all pass (~128s) |
| `go test ./internal/pages/...` (full package, both rounds) | pass |
| `go test ./...` (round 2, full repo) | pass |
| `guard-data-access.sh` / `guard-kiosk-engine.sh` | ✓ / ✓ |
| `guard-i18n.sh` | ✓ (no new user-facing strings; audit action strings render raw, not through `T`) |
| `guard-help-topics.sh` / `guard-compliance-claims.sh` | ✓ / ✓ |
| `guard-docs-shots.sh` | ✓ after `make docs-shots` |

No real client/shop name used as test data (existing fixture
`"Task Runner Cafe"`); no secret-shaped literal beyond the test PIN.

## Housekeeping

Both review rounds ran in an isolated `git worktree` (per this pipeline's
reviewer skill), never on the orchestrating checkout — each did its own
revert/mutate/restore cycles safely, with nothing committed or pushed
from either worktree.
