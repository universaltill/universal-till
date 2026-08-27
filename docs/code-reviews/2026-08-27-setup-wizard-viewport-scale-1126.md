# Code review: setup wizard and login screen weren't viewport-scaled

**Card:** universaltill/ut-docs#1126
**Date:** 2026-08-27
**Complexity:** medium — Dev inline (Sonnet), Review via an independent
Opus subagent (isolated worktree). One review round; findings were
correctness/test-quality/doc-staleness class, not money/tax/data-loss/
security, so a second round wasn't earned per this pipeline's
process-depth rule.

## What shipped

`web/ui/pages/setup.html` and `web/ui/pages/login.html` are "standalone
documents" (own `<head>`, don't extend `web/ui/layouts/base.html`) whose
`<html>` tag hardcoded `style="font-size: {{ uiscalepx }}px"` —
`uiScalePx()` (`internal/httpx/httpx.go`) returns a FIXED
`16 * currentUIScale()` px value, completely independent of viewport
size. This is the mechanism ut-docs#161 superseded for the sale screen
with a viewport-fluid one; setup/login never got that treatment, so on
the reported 1920x1200 Waveshare panel the wizard rendered at a fixed
16px root instead of the ~20px the fluid clamp would give it — about
8% of the screen, per the original report.

Fix (2 lines, no new CSS):

- `web/ui/pages/setup.html` and `web/ui/pages/login.html`: swapped the
  inline style to `style="--ui-scale: {{ uiscale }}"`, matching
  `web/ui/layouts/base.html`'s existing pattern exactly. app.css's
  already-global rule `html { font-size: calc(var(--ui-scale, 1) *
  var(--fluid-fs)); }` picks it up automatically — `.setup-card`,
  `.login-*`, `.picker-tile`, `.btn`, `.pin-key` are all already
  rem-based.
- `web/public/app.css`: updated the stale comment above that rule (it
  named login/setup as still using the fixed mechanism) to reflect the
  fix and point at the follow-up card for the remaining kiosk screens.

Deliberately out of scope: `self_order.html`, `self_order_shop.html`,
`order_tracking.html` carry the identical stale pattern (confirmed via
grep) but are customer-facing self-order kiosk screens with no existing
test coverage — filed as ut-docs#1175 rather than fixed blind here.

Regression coverage:

- `e2e/tests/login.spec.ts`: two new tests (setup wizard, login screen)
  each driving BOTH the 1024x600 kiosk floor and 1920x1200 Waveshare
  panel and asserting the root font-size differs between them (not just
  each independently against the old fixed 16px — see review finding F2
  for why that distinction mattered) and that a representative touch
  target (`.picker-tile`, `.pin-key`) still clears 44px.
- `internal/pages/auth_page_test.go`: new
  `TestLoginAndSetupUseFluidUIScaleCSSVariable` (added during review, see
  F2) — a Go-level render test asserting the response body carries
  `style="--ui-scale: 1.3"` at a non-default `UT_UI_SCALE` and does NOT
  carry the old fixed `style="font-size:` form. This is the property the
  e2e suite structurally cannot see (see F2).

## Independent review (Opus, fresh context, isolated worktree)

Ran the full gate itself (`go build`, `go vet`, `gofmt -l`,
`go test ./internal/pages/... ./internal/httpx/...`, `guard-i18n.sh`,
`guard-htmx-loaded.sh`, and all 17 CI `build`-job guards), re-verified the
TDD claim personally by reverting each html file individually (not both
at once — `describe.serial` aborts the block on first failure, so
reverting both together would only ever have proven the setup test
fails) and confirming each new e2e test fails with the exact expected
error, then restoring and confirming green. Also booted the real binary
with `UT_UI_SCALE=1.3` and read the raw response bytes for `/setup`,
`/login`, and (as an untouched control) `/self-order`, confirming the
rendered `--ui-scale` value genuinely reflects the operator's Settings
choice and that `self_order.html` is unaffected. Confirmed by exhaustive
grep that exactly six `<html>` root tags exist under `web/ui/` and the
change touches precisely the two in scope.

**Findings, all fixed in this same review round:**

- **F1 (high, CI-blocking):** `scripts/ci/guard-docs-shots.sh` hashes
  every file under `web/ui/**` into its manifest's `surface_sha256`, so
  editing these two templates invalidated
  `web/help/img/manifest.json` regardless of whether any *screenshotted*
  route's pixels changed. Confirmed by running the guard before and
  after reverting just the two html files. **Fixed:** ran
  `make docs-shots` (92/92 screenshots green) and committed the
  regenerated manifest. One PNG (`web/help/img/fa/users.png`) also
  changed — checked both images directly and they're visually identical
  (font-antialiasing-level noise from re-rendering, not a content
  change); `/users` doesn't reach either changed template, so this is
  expected re-render noise, not scope creep.
- **F2 (medium, false-pass gap):** the e2e assertions defeat a fix that
  hardcodes a *different* wrong constant, but not the most likely wrong
  fix — dropping the inline style attribute entirely. `run-till-auth.sh`
  never sets `UT_UI_SCALE`, so at the default scale of 1,
  `calc(var(--ui-scale, 1) * ...)`'s fallback makes "no attribute" and
  "`--ui-scale: 1`" indistinguishable to a browser assertion. Demonstrated
  live: with the style attribute removed entirely from both templates,
  all 13 e2e tests still passed. **Fixed:** added
  `TestLoginAndSetupUseFluidUIScaleCSSVariable` (`internal/pages/auth_page_test.go`)
  — a Go-level test at a non-default `UT_UI_SCALE=1.3` asserting the
  literal `--ui-scale: 1.3` string in the response body, which the
  attribute-dropped variant cannot satisfy. TDD-verified personally
  (reverted the two html files, confirmed this new test fails with the
  claimed error, restored, confirmed it passes).
- **F3 (medium, latent flake):** the new setup-wizard e2e test picked the
  country tile via `.locator('button.picker-tile').first()` with no
  `:visible` filter and a one-shot `isVisible()` check before tapping
  "Show all countries" — setup.html keeps every tile in the DOM under
  `x-show` (hidden via `display:none`, not removed), so on a host where
  OS-locale detection resolves to a real country, `showAllCountries`
  starts `false` and `.first()` in DOM order can resolve to a
  permanently-hidden tile. The file's own neighbouring tests already
  document and avoid exactly this trap. **Fixed:** switched to
  `.locator('button.picker-tile:visible').first()` plus an explicit
  `await expect(tile).toBeVisible()` before reading `boundingBox()`,
  matching the established idiom elsewhere in this file.
- **F4 (medium, stale docs):** app.css's own comment directly above the
  `--fluid-fs`/`--ui-scale` rule still named login/setup as part of "the
  few standalone screens ... that still set an inline px font-size
  directly" — now false, and exactly what the ut-docs#1175 follow-up's
  implementer would read first. **Fixed:** updated the comment to name
  the three still-affected kiosk screens and point at ut-docs#1175.
- **F5 (low, comment nit):** the 44px touch-target assertions' comment
  overstated its own coverage ("only holds once the root font-size
  actually scales") — `.picker-tile`'s 3.2rem min-block-size already
  clears 44px even at the old fixed 16px root, so that assertion passed
  before the fix too and doesn't itself regression-guard the scaling
  behaviour; the font-size comparison assertions are what do that.
  **Fixed:** softened the comment to say so plainly.

## Verified beyond automated tests

- Screenshots taken and actually looked at (not just asserted): `/setup`
  at 1024x600 and 1920x1200 (English/LTR) — clean, card visibly larger
  at the bigger viewport, no overlap/clipping/misalignment; `/setup?lang=fa`
  at 1024x600 (RTL) — correctly mirrored, no defects.
- Dark theme: not applicable — `setup.html`/`login.html` have no
  theme-stylesheet conditional at all (unlike `base.html`-extending
  pages), so there is no dark-theme surface on these two pages.
- `web/help/`: no manual-topic prose update needed — `/setup` and
  `/login` are both claimed by `web/help/en/users.md`
  (`routes: [/users, /pin, /login, /setup, /users/permissions]`), and
  `docs-shots` only screenshots a topic's `routes[0]` (`/users`, a
  `base.html` page untouched by this change). No step or screenshot in
  that topic describes anything this diff changed.

## Explicitly deferred

- **Real Pi 5 touch verification** (the AC item "verified on the real
  Pi 5 touchscreen, by touch") — no physical hardware access from this
  cloud session. Same precedent as ut-docs#1107/#1078/#1102.
- **`self_order.html`, `self_order_shop.html`, `order_tracking.html`**
  carry the identical stale pattern — tracked as ut-docs#1175, not fixed
  here (different hardware/UX context, no existing test coverage).

## Safe-to-merge verdict

Yes. Full gate green (`go build`, `go vet`, `gofmt -l`,
`go test ./internal/pages/... ./internal/httpx/...`, all 17 CI
`build`-job guards including the now-fixed `guard-docs-shots.sh`), 15
e2e tests green (`login.spec.ts`, `--project=auth`), both TDD claims
(e2e and the new Go test) independently re-verified fail→pass. No real
client/shop name introduced; no secret-shaped values in this diff.
