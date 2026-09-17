# Code review — theme change reaches an already-open till live (ut-docs#2343)

- **Date:** 2026-09-16
- **Ticket:** ut-docs#2343 (product owner report: "I changed the theme in
  cloud.universaltill.com, and it said applied but the theme didn't change
  on tills. Also changed one of the till names which didn't change as
  well.")
- **Branch:** `fix/2343-theme-sync-live-till`
- **Reviewer:** independent pass, Opus subagent — different model from the
  Sonnet implementation, never saw the dev reasoning; ran cold against the
  branch's `git diff`.
- **Verdict on the second (redesigned) draft: SAFE, pending a human decision
  on `ut-docs#2277`** (see "Not merging" below — unrelated to this diff's
  own correctness).

## Investigation (BA)

Two candidate defects named in the ticket, verified rather than assumed:

1. **Theme.** Real bug. The cloud's `set_setting` directive (ADR-0018)
   already lands the new theme in the till's live `d.State` the moment it's
   applied — `SetSetting` → `rederive` → `common.LoadState`, exactly the
   path a local Settings-page change takes (traced end to end, confirmed
   `KeyTheme` round-trips through both). The gap: nothing tells an
   *already-open* kiosk session to pick it up. The local Settings page
   forces this with its own `window.location.reload()`
   (`web/ui/pages/settings.html`); a directive landing in the background has
   no client to tell.
2. **Till rename.** Not a code defect. `ut-cloud`'s `claims.Service
   .RenameDevice` is a deliberate, well-tested, cloud-side-only display
   label (its own doc comment explains why: writing the till's live `name`
   field directly would be clobbered by the till's own next heartbeat) —
   confirmed correctly persisted and redisplayed via existing tests
   (`TestRenameDevice`, `TestHandleRenameDevice`). Split out as a separate
   card, ut-docs#2351 (Admin Review — a product decision on whether it
   should ever push to the till, not an engineering one).

## What shipped (scope: theme only)

- `internal/pages/themes.go` — `registerThemeSync` wires `GET
  /ui/theme-sync`. Unconditionally reports the live theme in a body-safe
  OOB `<div id="theme-sync-poll" data-theme="...">`, re-asserting its own
  `hx-get`/`hx-trigger`/`hx-swap` on every response (see "Findings" #2 below
  for why that repetition is load-bearing, not decoration).
- `web/ui/layouts/base.html` — `#theme-css` (the theme override
  `<link>`) is now always rendered (even for `"default"`, which has no
  override CSS of its own) and carries `data-theme` recording what it
  currently shows. A poll `<div id="theme-sync-poll">` (mirrors the
  existing `#pairing-notice-mount` pattern) plus a small
  `htmx:oobAfterSwap` listener that applies a changed `data-theme` to
  `#theme-css` — never a page reload, so an in-progress sale's on-screen
  state is never disturbed (the offline-first "checkout must never be
  blocked" rule applied to a forced reload, not just to the network).
- `internal/pages/theme_sync_test.go` — 4 handler/render-level tests.
- `e2e/tests/theme-sync-2343.spec.ts` — 2 real-browser Playwright specs.
  **Load-bearing, not belt-and-braces**: see Findings below — both real
  bugs in this feature were invisible to every server-string-assertion
  test and were only caught by actually driving it in Chromium.

## Findings

This card went through **two rounds** because the first was a genuine
process failure, not a nitpick round — recorded here for the same reason
`till-settings-directive-set-till-setting.md` records its own findings:
so the next reviewer doesn't have to rediscover the failure mode.

1. **BLOCKER (round 1, fixed in round 2) — the shipped mechanism was a
   complete no-op.** The first draft's `GET /ui/theme-sync` emitted the OOB
   fragment as a bare `<link id="theme-css" hx-swap-oob="true" ...>`
   directly. htmx's response parser runs the fragment through
   `DOMParser.parseFromString(resp, "text/html")` and reads back
   `responseDoc.body` — a `<link>` (a head-only element) with no
   surrounding `<html>/<head>/<body>` gets hoisted by the HTML parsing
   algorithm into a synthetic `<head>`, leaving `body` empty.
   `handleOutOfBandSwaps` only ever scans `body`, so the swap silently did
   nothing. Verified live: 5 polls, zero `/themes/*.css` requests, no
   console error, `background-color` never changed. A `<span>` control in
   the same harness swapped fine; reordering the response to put the
   `<span>` first made even the `<link>` swap work, isolating the cause to
   head-hoisting, not to OOB or `hx-swap="none"` in general.
   **All 5 of round 1's tests passed anyway** — every one of them asserted
   the *response string*, never htmx's actual processing of it, so the
   suite was green while the ticket's exact symptom reproduced unchanged.
2. **Fixed in round 2, caught only by the real e2e spec — the redesigned
   response killed its own polling.** Routing the value through a
   body-safe `<div>` instead fixed finding 1, but OOB's default swap mode
   is `outerHTML`: it replaces the *entire* target element, not just its
   attributes. The very first response (which carried only
   `hx-swap-oob="true" data-theme="..."`, no `hx-get`/`hx-trigger`) wiped
   the polling div's own trigger wiring on the first swap, so it never
   polled again — silently missing every future change. Fixed by having
   the response div re-assert `hx-get="/ui/theme-sync" hx-trigger="every
   30s" hx-swap="none"` on itself every time.
3. **Fixed in round 2, also caught only by the real e2e spec — the
   listener read a stale event target.** `htmx:oobAfterSwap`'s
   `e.detail.target` is the *pre-swap* element on a page's own first
   "load"-triggered poll (no `data-theme` at all, since the initially
   rendered div carries none) — not the just-inserted replacement. Reading
   it directly made every single page load look like "theme changed to
   empty" and wrongly cleared `#theme-css`'s `href`, immediately after the
   page had just rendered the *correct* theme. Fixed by re-reading
   `#theme-sync-poll` fresh via `getElementById` inside the listener
   instead of trusting the event's own target — confirmed (via a
   throwaway debug instrumentation pass, since removed) that a fresh
   lookup always reflects the just-swapped DOM correctly even when
   `e.detail.target` doesn't.
4. **Non-issue, verified — XSS defence.** `html.EscapeString` on the theme
   value going into `data-theme="..."` is sufficient (the attribute is
   never used to build a `href` string server-side any more; the client
   builds the href from `dataset.theme`, itself read from a
   browser-parsed, already-escaped attribute).
5. **Non-issue, verified — auth/kiosk isolation.** `/ui/theme-sync` is
   session-gated like every other `base.html` page (not in the auth-exempt
   set); the customer-facing self-order kiosk never includes `base.html` at
   all (its own template has no theme override), so it is unaffected by,
   and unreachable through, this endpoint. `guard-kiosk-engine.sh`
   confirmed green — the handler touches only `d.CurrentState().Theme`, no
   `Engine`/basket.
6. **Non-issue, verified — the "revert to default" edge case.**
   `live == "default"` still special-cases correctly on the *server* side
   test (`TestThemeSync_ReportsDefaultThemeAsIs`); the *client*-side
   href-removal logic (`live !== 'default'`) is exercised by the e2e specs
   indirectly (starting theme is always a real, non-default one there) —
   not separately e2e-tested for the revert direction specifically, noted
   as a residual, low-risk gap rather than folded in (reverting to default
   is the least urgent direction: it degrades to "no CSS override", not a
   stuck stale theme).

## What the reviewer / re-verifier personally ran

- `gofmt -l .`, `go build ./...`, `go vet ./internal/pages/...`,
  `golangci-lint run ./internal/pages/...` (0 issues), full
  `go test ./...` (all green, `internal/pages` genuinely executing at
  ~130s), `go test ./internal/pages/... -run TestThemeSync|TestIndexPage`
  (all 4 targeted tests green).
- Guards: `guard-i18n.sh`, `guard-data-access.sh`,
  `guard-page-http-error.sh`, `guard-kiosk-engine.sh`,
  `guard-htmx-loaded.sh`, `guard-help-topics.sh`,
  `guard-e2e-fixtures-import.sh`, `guard-docs-shots.sh` — all green.
- **Two full, real Playwright specs against the live app**
  (`e2e/tests/theme-sync-2343.spec.ts`), each independently re-run after
  every fix, using the repo's own vendored `htmx.min.js` and a real
  Chromium: (a) a theme changed via a plain POST to
  `/api/settings/theme` (standing in for the cloud directive, which writes
  through the identical `SetSetting`/`Settings.Set` path) reaches a
  second, already-open, never-reloaded page within one forced poll, and an
  unsaved scan-input value on that page survives — proof no reload
  happened; a repeated poll after settling does not re-fetch the
  stylesheet — proof finding 2's resend loop is actually closed; (b) a
  fresh page load correctly keeps (does not clear) its own already-correct
  theme through its own first automatic poll — the regression test for
  finding 3.
- **`make docs-shots` run twice**, once with this diff and once on a clean
  checkout of its parent commit, confirming the identical 47-PNG drift
  either way (pre-existing Chromium/environment font-rendering
  nondeterminism in this sandbox, unrelated to this change) — only
  `surface_sha256` genuinely differs because of this diff, refreshed via
  `update-docs-shots-surface-hash.sh`.

## Not merging this automatically

`ut-docs#2277` (Admin Review, unresolved as of this diff) asks whether this
pipeline's standing auto-push authorization ("no real users yet") still
holds, given evidence of a live till fleet. Several sibling PRs opened the
same day are explicitly holding for that answer rather than merging; this
one does too — pushed and open for CI, deliberately not merged pending a
human decision on that card.

## Deferred / follow-up

- ut-docs#2351 — cloud panel till-rename is a display-only label; product
  decision on whether it should also push to the till.
- The "revert to default" client-side path (finding 6) has no dedicated
  e2e coverage; low-risk, noted rather than blocking.
