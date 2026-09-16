# Code review: /open-orders resume auto-park notice (ut-docs#2193)

**Date:** 2026-09-16
**Card:** ut-docs#2193
**Complexity:** medium (Dev: Sonnet inline; Review: Opus, fresh subagent, isolated worktree)

## What shipped

`/open-orders`' own resume route (`POST /open-orders/resume`,
`internal/pages/open_orders_page.go`) auto-parks a busy live basket instead
of refusing (ut-docs#1919's behavior), but its full-page redirect gave zero
on-screen cue — unlike the sale-screen popup's htmx fragment path
(`hold_api.go`), which already tells the cashier via the
`hold.toast.parked_and_resumed` toast.

The fix wires two already-shipped primitives together instead of inventing
anything new:

- `httpx.QueryMsgKey` (`internal/httpx/httpx.go`, added for ut-docs#2148,
  previously unused) reads and validates a `?msg=<key>` query param against
  real i18n keys, falling back to a generic key on garbage.
- `web/ui/pages/index.html`'s existing `tseKickoffRejected` one-shot-banner
  pattern, cloned for a success-styled banner
  (`data-testid="resume-notice-banner"`).

No new i18n keys — reuses `hold.toast.parked_and_resumed`, already shipped
in every core locale (en/fa/ar/tr) since ut-docs#1919. No ADR — Architect's
call: pure reuse of two already-accepted primitives, no new mechanism.

Files touched:
- `internal/pages/open_orders_page.go` — new `resumeOKParkedPrior` case
  redirecting to `/?msg=hold.toast.parked_and_resumed`, instead of falling
  into the generic `default: redirect to "/"`.
- `internal/pages/index_page.go` — `"/"` handler's template data gains
  `"msgKey": httpx.QueryMsgKey(r)`.
- `web/ui/pages/index.html` — new one-shot `.pos-notice success` banner.
- `internal/pages/open_orders_page_test.go` — updated
  `TestOpenOrdersResume_BusyBasketIsAutoParkedThenTargetOpens`'s Location
  assertion.
- `internal/pages/sale_screen_resume_notice_test.go` — new: banner
  render/no-render, translated text, `role="status"`, unrecognised-key
  fallback.
- `web/help/img/manifest.json` — `surface_sha256` refreshed via
  `scripts/ci/update-docs-shots-surface-hash.sh` (see below).

## Independent review

Opus, fresh context, isolated worktree (never saw the Dev reasoning).
**Verdict: PASS WITH FIXES.**

### Finding (fixed)

The diff trips `guard-docs-shots.sh`'s whole-file surface hash — it hashes
every file under `web/ui/**` and non-test `internal/pages/**.go`, and this
diff touches three of them. The reviewer did not assume the guard's
escape hatch applied; it verified byte-for-byte that the change alters no
rendered pixel on the screenshotted bare `/` page: the only delta is 7
bytes of inter-element whitespace left by the new
`{{ if .msgKey }}`/`{{ end }}` template lines, which `html/template`
collapses to nothing inside the surrounding block box
(`<main class="container">`). `/open-orders`'s own GET handler is
byte-unchanged. Applied the guard's own documented escape hatch
(`scripts/ci/update-docs-shots-surface-hash.sh`) rather than a full
`make docs-shots` regeneration — confirmed the resulting `manifest.json`
diff is exactly the one `surface_sha256` field, and the guard now passes.

### TDD re-verification (done for real, not taken on trust)

Reverted the three production-code files in the isolated worktree while
keeping the tests, ran the affected tests, confirmed:
- `TestOpenOrdersResume_BusyBasketIsAutoParkedThenTargetOpens` → FAIL:
  `expected a redirect to the sale screen carrying the parked-and-resumed
  notice, got "/"`
- `TestSaleScreen_ResumeNoticeBannerRendersFromMsgParam` → FAIL: banner
  absent from the dumped response body

Restored the production files, confirmed both pass, and confirmed the
unmodified `TestOpenOrdersResume_SuccessRedirectsToSaleScreenWithBasketLoaded`
(plain resume, still asserts bare `/`) still passes — no regression.

### Correctness checks

- **Switch exhaustiveness**: `resumeOKParkedPrior` is only returned when
  `d.Engine.HasItems()` was true *and* the auto-park succeeded; the
  same-order no-op case returns plain `resumeOK` before that check, so
  re-tapping the live order cannot spuriously trigger the notice.
- **No unescaped `?msg=`**: `httpx.T` returns a plain `string`, so
  `html/template` escapes it as a text node; `QueryMsgKey` independently
  validates against the real key set before that. No `template.HTML`/
  `template.JS` near it.
- **Kiosk isolation**: the `self_order` mode redirect (`index_page.go`)
  fires before the template data map (and `msgKey`) is ever built —
  confirmed empirically (`GET /?msg=…` in `self_order` mode still 303s to
  `/self-order`, banner never rendered; same for `backoffice` mode).
  `guard-kiosk-engine.sh` passes; the diff touches no `/self-order` route
  and reads no `Engine` there.
- **RTL/i18n**: `.pos-notice`/`.notice-dismiss` use only logical
  properties (no literal left/right); no new key, so no
  `ut-plugin-language-*` follow-up needed.
- **File-write / cwd-path bug classes**: not applicable — the diff adds no
  `os`, filesystem, or path calls.
- **Test-integrity**: confirmed the new test's translated-text assertion is
  not vacuous — `internal/pages/main_test.go`'s `TestMain` wires real
  locales for the whole test binary, and `httpx.T("en", …)` returns the
  real English string when run in isolation.
- **Banner lifecycle**: `app.js`'s existing `scheduleToastDismiss` sweeps
  every non-error `.pos-notice` on `DOMContentLoaded`, so the new banner
  auto-expires the same way the htmx-path toast does, with no new JS.
- **`/ui/buttons` renderer** (also renders `index.html`'s content block,
  with a data map lacking `msgKey`): `{{ if .msgKey }}` on a missing key is
  safely falsy. `internal/ui` tests pass.

### Deferred (real, out of scope — filed as ut-docs#2347)

Both resume outcomes redirect to a hardcoded `"/"` rather than the
existing `saleScreenReturnURL(mode)` helper (`index_page.go`, already used
by `/open-orders`' own "Back to sale" link) — so the notice, and the
resumed basket itself, is silently dropped on a `backoffice`- or
`self_order`-mode till. Confirmed pre-existing for the *plain* resume case
too, not introduced by this change.

Also noted, not filed (accepted as-is): a crafted `?msg=<any real i18n
key>` can render an arbitrary real translated string in a green success
banner — inherent to `QueryMsgKey`'s design (validates key existence, not
banner-appropriateness), identical to `fiscal_device_page.go`'s existing
`?msg=` usage; not a new hole.

## Manual (`web/help/`)

No update needed. `web/help/en/open-orders.md` step 2 already describes
this exact behavior ("If you already have a sale going, it is held for you
first … so nothing you rang up is lost"). This change adds on-screen
confirmation of already-documented behavior — no new screen, no new
capability, no changed steps — and the docs-shots harness screenshots bare
`/`, where the one-shot banner never appears, so no screenshot is affected.

## Gate (full repo)

`gofmt -l .` clean · `go build ./...` OK · `go test ./...` (whole repo)
green · `golangci-lint run ./...` 0 issues · `guard-i18n.sh`,
`guard-data-access.sh`, `guard-help-topics.sh`, `guard-kiosk-engine.sh`,
`guard-docs-shots.sh` (after the surface-hash fix) all pass.

## Verified beyond automated tests

Tester drove the real app (a throwaway till booted via `e2e/run-till.sh`,
headless Chromium via Playwright): confirmed the redirect target, the
banner's text/styling/role, dismiss behavior, one-shot behavior (clears on
navigating to plain `/`, matching the existing `tseKickoffRejected`/`?err=`
pattern — a literal reload of the exact `?msg=…` URL re-shows it, which is
the same established behavior those banners already have, not a
regression), the unrecognised-`?msg=` fallback, and RTL rendering at 360px
(fa locale, dismiss button correctly mirrored). Not independently
screenshotted: ar/tr locales, real touch hardware, kiosk display mode
specifically — low risk, as this reuses markup/classes already proven in
all three contexts by the `tseKickoffRejected`/`fiscalOverrideActive`
banners it's modeled on.

## Verdict

**Safe to merge.**
