# Code review: `Vary: HX-Request` on every dual-mode route (ut-docs#2091)

**Date:** 2026-09-11
**Card:** universaltill/ut-docs#2091
**Branch:** `fix/2091-vary-hx-request`
**Complexity:** medium (Dev: Sonnet inline, Review: Opus, fresh-context isolated-worktree subagent)

## What shipped

Six GET routes in `internal/pages` return two different bodies for the
same URL depending on the `HX-Request` request header — a bare htmx
fragment, or a complete standalone page: `/catalog`, `/modifiers`,
`/catalog/option-sets` (`internal/pages/catalog/handlers.go`),
`/categories` (`categories_page.go`), `/inventory` (`inventory_page.go`),
and `/help/{topic}` (`help_page.go`). None of them set a `Vary` header, so
a browser/WebView HTTP cache keyed purely on the URL could serve a
fragment cached from an htmx rail-swap navigation back to a later plain
navigation to the same URL — the product owner's report: pressing the
Android hardware Back button after visiting Catalog through the `/items`
rail landed on "a none-styled page" (a bare fragment with no `<head>`, no
stylesheet, no nav).

Fix: `internal/httpx.IsFragmentSwap` — the single helper every one of
these six routes already calls to decide which branch to render — now
takes `(w http.ResponseWriter, r *http.Request)` and sets
`Vary: HX-Request` on `w` unconditionally as its first action, before
either branch renders. All five existing GET call sites were updated to
pass `w`. `help_page.go`'s `renderHelpPage` previously carried its own
separate inline copy of this exact check (predating the shared helper,
ut-docs#433) — retired in favor of calling `httpx.IsFragmentSwap(w, r)`
directly, which both fixes `/help/{topic}` and removes a second place
this rule could go stale.

## Correction to the issue's own text

The issue's AC #3 claimed `/help/{topic}` also serves
`Cache-Control: public, max-age=3600` (citing `help_page.go:81`) and
asked for that to be revisited. Verified false during BA and independently
re-verified by the reviewer: that header belongs to the separate,
single-mode `GET /help/img/{locale}/{file}` static-PNG handler (lines
63–83), not to `renderHelpPage` (lines 90–158), which sets no
`Cache-Control` at all. AC #3 was dropped as inapplicable rather than
implemented — there was nothing there to change, and the PNG route's
existing public caching is correct and untouched.

## Independent review (Opus, isolated worktree, different model from the Sonnet implementation)

**Verdict: safe to merge, no blocking findings.**

Confirmed independently, not just re-read from the implementer's claims:

- Re-derived the Cache-Control misattribution by reading `help_page.go`
  directly — confirmed correct.
- **Re-ran the TDD red→green cycle personally**, in the isolated
  worktree: reverted the 5 production files
  (`internal/httpx/httpx.go`, `internal/pages/catalog/handlers.go`,
  `internal/pages/categories_page.go`, `internal/pages/inventory_page.go`,
  `internal/pages/help_page.go`) to `main`, re-ran the new tests, and got
  real failures with the exact reported bug signature —
  `TestCatalogFragmentCache_VaryPreventsFragmentServedToPlainNavigation`
  failed with the cached fragment body (`<div class="page-head">…`, no
  `<html>`/`<head>`) being served to the plain-navigation request; all
  five Vary-assertion tests failed with `Vary header = "", want
  "HX-Request"`; `internal/httpx` failed to *compile* against the old
  1-arg signature, exactly as expected. Restored the fix and confirmed
  every test green again.
- Confirmed `Vary` is set on both branches at all 6 routes, unconditionally
  and before any branch writes a body, by reading each of the 6 call
  sites directly.
- Confirmed the `help_page.go` refactor preserves identical behavior
  (the removed `isHistoryRestore` variable had exactly one other use site,
  now folded into the same `httpx.IsFragmentSwap` call).
- Grepped for a possible 7th missed dual-mode route — none found; the
  other `HX-Request` references in the codebase are either POST-mutation
  responses (a different, unrelated concern) or an in-process
  sub-request header that's never observed by an HTTP cache.
- Confirmed the diff is Go-only (`git diff --stat` — no template/markup/
  CSS), so no manual/help-topic update is owed and no UX-guideline check
  applies.
- Full gate re-run independently: `go build ./...`, `go vet ./...`,
  `gofmt -l .`, `golangci-lint run` (0 issues), the touched-package test
  suites, and the full `go test ./...` (57 packages, all green) — all
  green in the isolated worktree, matching the implementer's own run.
- `guard-data-access.sh`, `guard-page-http-error.sh`,
  `guard-help-topics.sh` all green.
- No real client/shop name, no secret-shaped literal, anywhere in the diff.

**Non-blocking observation (deliberately not fixed, not worth a Backlog
card):** `IsFragmentSwap` uses `Header().Set("Vary", ...)`, which would
overwrite (rather than merge with) a `Vary` value set by some future
upstream middleware. No such middleware exists anywhere in this codebase
today (verified by grep — this line and the tests are the only `Vary`
producers), so this is speculative hardening, not a live defect. Noted
here so it isn't independently rediscovered as a "finding" later; revisit
only if/when a real cross-cutting `Vary`-setting middleware is ever added.

## Verified beyond automated tests

- **Real driven verification against the actual running binary** (not
  just Go's `httptest`): built the real server binary, seeded a throwaway
  SQLite DB via `scripts/e2e_seed`, ran with `UT_AUTH=off`, and `curl`'d
  all 6 routes directly. Confirmed via genuine HTTP responses:
  `Vary: HX-Request` present on both the plain and `HX-Request: true`
  response for every route; the full-page body actually contains `<html>`
  and the fragment body does not (a real behavioral difference, not just
  a header assertion); confirmed `/help/img/en/voucher-import.png` (the
  route the issue misattributed) is untouched — still 200,
  `Content-Type: image/png`, `Cache-Control: public, max-age=3600`, no
  `Vary`. Server killed and its throwaway data directory left in `/tmp`
  (not part of the repo).
- Playwright e2e suite intentionally not run: this diff touches no
  template/markup/CSS, so there is no new visual surface for it to catch;
  the actual caching mechanism was verified more directly (a real running
  server plus a Vary-aware cache simulation in Go) than a browser e2e run
  would verify it.
- **Not verified:** real Android/WebView hardware (the issue's own AC #5).
  No device available in this cloud pipeline session — documented gap,
  not a merge blocker, consistent with this pipeline's standing
  convention for cloud-session device-verification gaps.

## Scope

No i18n/locale files touched, no money/plugin/offline-first surface, no
ADR needed (mechanical bug fix to existing behavior, not a new
architectural decision). Sibling issue ut-docs#2090 (rail-loss/IA defect,
reported alongside this one) is untouched — independent, explicitly out
of scope per that issue's own note.
