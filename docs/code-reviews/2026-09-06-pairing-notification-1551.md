# Review: nav-level dismissible notice + badge for pending pairing requests (ut-docs#1551)

## What shipped

A manager anywhere in the app now sees a pending till-pairing request
within one 30s poll, instead of having to navigate to `/tills` and
refresh:

- **`GET /ui/pairing-notice`** (`internal/pages/pending_pairings.go`) —
  a new nav-level fragment, mounted once in `web/ui/layouts/base.html`
  as a sibling of `<nav>`/`<main>` (the same "placeholder + htmx
  fragment" convention `nav.html` already uses for the sync/bugreport/
  session/fiscal chips — those have no per-request data at page-render
  time, so a fragment loads after render and on every poll). Renders a
  dismissible banner (`web/ui/partials/pairing_notice.html`) with the
  pending count and a link to `/tills`; empty on a replica till (pairing
  approval only ever happens on the primary), for a caller without
  `sync_management`, or when nothing is pending.
- **`GET /ui/sync-chip`** (`internal/pages/sync_admin.go`) — the
  existing rail chip's primary-side branch now also folds the pending-
  pairing count into its warn/badge state and its empty-roster early-
  return guard (mirroring ut-docs#1133's identical fix for quarantined
  entries), so the shop's very first pairing attempt — before any till
  is enrolled — still shows.
- Both surfaces auto-clear the moment a request is approved, denied, or
  expires: `PairingRepo`'s pending queries already exclude those. The
  notice's dismissal persists across polls via a client-side
  `sessionStorage` fingerprint of the pending ID set (same try/catch
  pattern as `bugreport_panel.html`'s `DISMISS_KEY`), so approving one
  request while a second is still pending, or a genuinely new request
  arriving, changes the fingerprint and the notice reappears.
- New `PairingRepo.ListPendingReadOnly` (`internal/data/pairing_repo.go`)
  — see "What the independent review found" below.
- i18n keys added to `en`/`ar`/`fa`/`tr`
  (`sync.chip_pairing_pending_one/other`,
  `tills.pairing.notice_device_one/other`, `notice_link`,
  `notice_waiting`; reuses the existing generic `notice.dismiss` key for
  the dismiss button). `web/help/en/multitill.md` and the regenerated
  `web/help/img/` manifest/screenshots updated in the same branch
  (`make docs-shots`).

Tests: `internal/pages/pending_pairings_test.go` (5 new tests covering
the manager gate, empty state, count/fingerprint rendering, the
replica guard, and the mount-markup regression below),
`internal/pages/sync_admin_test.go` (1 new test for the pending-pairing
badge with zero enrolled tills).

## Independent review (Opus, isolation: worktree) — a real blocker, fixed before merge

The first draft mounted the notice placeholder in `base.html` with
`hx-swap="outerHTML"`. htmx's `outerHTML` swap **replaces the element
carrying the swap's own `hx-trigger`** — so the very first poll that
returned an empty body (the normal "nothing pending" case) destroyed the
placeholder along with its own polling trigger, permanently stopping all
future polling. The reviewer proved this in a real headless-Chromium run
against the repo's own vendored `htmx.min.js`, A/B against the working
`nav.html` chip pattern:

- **Scenario A** (nothing pending at load, a device then requests to
  pair): only one poll ever fired; the notice never appeared for a
  request arriving after page load.
- **Scenario B** (a request pending at load, then approved): only one
  poll ever fired; the banner stayed on screen indefinitely — the
  literal "must clear itself... never a stale notice" acceptance
  criterion, violated.

Both are exactly the case ut-docs#1551 exists to fix, and both were
invisible to every automated test and CI guard (all of which target the
*handler*, not the *mount* — the reviewer noted this explicitly: "the
full green test/guard suite is misleading here").

**Fix:** the placeholder now uses the default (`innerHTML`) swap, same
as `nav.html`'s `#sync-chip`/`#bugreport-chip` spans, under a
**different id** (`pairing-notice-mount`) from the rendered partial's
own root (`pairing-notice`) — with `innerHTML` the two coexist as nested
elements, and sharing an id would have made the partial's own dismiss
script (`getElementById('pairing-notice')`) resolve ambiguously, at best
to the id-less-in-practice placeholder rather than the real banner
(breaking the fingerprint/dismiss logic in a second, independent way).

Re-verified in a real browser through **actual 30s htmx poll cycles**
(not a manually-triggered `htmx.trigger` call, which is how the first
manual verification pass missed this): a request created after page
load now appears within one real poll; approving it clears the notice
within the next real poll. Also added
`TestPairingNoticeMount_KeepsPollingAndUsesADistinctID`
(`internal/pages/pending_pairings_test.go`) — a markup-level regression
test asserting `base.html`'s placeholder tag never re-introduces
`hx-swap="outerHTML"` and never shares an id with the partial's root,
since the bug was invisible to every handler-level test that already
existed.

### Other findings, triaged

- **Fixed — avoidable write on a hot read path.** `PairingRepo.ListPending`
  unconditionally runs `DELETE FROM pending_pairings WHERE expires_at < ?`
  before every read. Before this review both `/ui/sync-chip` and the new
  `/ui/pairing-notice` called it every 30s, per open page, per manager —
  SQLite write-lock contention against sale writes that didn't exist
  before this change (offline-first: checkout must never be blocked).
  Added `ListPendingReadOnly` (filters `expires_at` in the `WHERE`
  clause instead of deleting), used by both nav-level pollers; the
  manager-facing approve/deny queue (`GET /ui/tills/pending-pairings`)
  keeps calling the original write variant on its own identical cadence,
  so expired rows are still swept regularly.
- **Fixed — unstable fingerprint tie-break.** `requested_at` is
  RFC3339-second precision with no supporting index, so two requests
  landing in the same second could sort differently across polls,
  making a dismissed notice reappear despite nothing having changed
  (fails safe — over-showing, never hiding a real request — but still
  wrong). Both `ListPending` and `ListPendingReadOnly` now add
  `, id ASC` as a stable tie-break.
- **Fixed — error-handling convention.** The notice handler originally
  answered a `PairingRepo` read error with a `500` + localized error;
  htmx doesn't swap a non-2xx response, so the only visible effect was
  logging an error every 30s per open page for as long as the read
  stayed broken. Changed to the same silent-empty-on-error convention
  the sibling nav fragments (`/ui/sync-chip`, `/ui/bugreport-chip`)
  already use.
- **Fixed — inaccurate help-topic wording.** `multitill.md` originally
  said dismissing hides the notice "until a new request arrives or the
  till is reloaded" — the mechanism is `sessionStorage`, which
  *survives* a reload and only clears when the till app restarts.
  Corrected.
- **Accepted as-is — `.pairing-notice`'s hardcoded blue tint.** Matches
  `.pos-notice.info`'s existing rule verbatim (deliberate, and the
  in-file comment says so) rather than a new inconsistency; a broader
  theme-token pass for that whole notice-color family is out of scope
  here.
- **Confirmed, not a finding — ARIA role, kiosk isolation, i18n, RTL,
  repository pattern.** All independently re-checked by the reviewer
  and confirmed clean: `role="status"` matches the codebase's own
  info-vs-error convention; `/self-order` renders through its own
  standalone template, never `base.html`, so the notice cannot leak
  there; all 6 new locale keys are genuine translations present in
  every locale file; `.pairing-notice` uses only logical CSS properties;
  no SQL outside `internal/data`.

## Verified beyond automated tests

- Real driven run against the live app (`go run .`, `UT_AUTH=off`):
  created a pairing request via the real API, fetched
  `GET /ui/sync-chip` and `GET /ui/pairing-notice` directly, confirmed
  both fragments render the expected markup, then approved the request
  and confirmed both go empty immediately.
- Screenshots taken of `/tills` with a pending request, in **English**
  and **Persian (RTL)** at 1024×700 (the kiosk viewport) — notice banner
  and rail badge both visible, correctly mirrored in RTL, no overlap or
  clipping. Dark/other themes not separately captured (the notice
  deliberately reuses `.pos-notice.info`'s existing, already-shipped
  palette handling rather than introducing a new one).
- Post-review: re-ran the same live-app check through **real 30s htmx
  poll cycles** (see "Independent review" above) to prove the fix,
  not just the original manual fragment fetch.
- `go build ./...`, `go vet ./...`, `gofmt -l .`, full `go test ./...`,
  `golangci-lint run ./...` (0 issues), and every guard listed in
  `universal-till/CLAUDE.md`'s "Before committing" section — all clean
  after the fix round.

## Not covered / explicitly deferred

- No new e2e Playwright spec was added for this surface (the existing
  `docs-shots` Playwright suite exercises it incidentally via the
  `/tills` topic screenshot, but that's steady-state, not a poll-cycle
  assertion). The Go-level markup regression test plus the manual real-
  poll browser verification above are the coverage for the specific bug
  class found; a dedicated e2e spec asserting the poll behavior would be
  a reasonable follow-up if this class of bug recurs elsewhere.
- Dark theme and non-default theme plugins were not separately
  screenshotted (see "Accepted as-is" above).

## Verdict

**Safe to merge.** The one blocker the independent review found was
fixed and re-verified through the exact failure mode it originally
demonstrated (real poll cycles in a real browser, not just handler-level
assertions); the four minor findings were all fixed; the two accepted
items are genuinely out of scope or already-precedented. Full CI gate
green.
