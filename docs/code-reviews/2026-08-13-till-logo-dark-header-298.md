# Review — till logo dark-header fix (ut-docs#298)

## Summary

The till's top nav bar rendered the brand mark as a white rounded-square
tile pasted on the dark navy header — the product owner's report was a
screenshot of exactly that "patch pasted on" look. Root cause: the
canonical mark (`unitill-logo.svg`) is a solid-black silhouette on a
transparent background, and `.logo`'s CSS gave it a white plate so it
would stay visible against `.nav`'s dark `var(--brand)` background.

Product owner's final decision (2026-08-13, on the issue): a dedicated
light/white-glyph, transparent-background variant of the mark, used by
the till; cloud.universaltill.com (a separate repo) keeps its existing
dark mark unchanged.

## Investigation before writing code

- `.nav`'s background is `var(--brand)`, which every shipped theme
  (default/fresh/slate/amber/monarch) sets dark navy/near-black — `.nav`
  is the **one** surface guaranteed dark in every theme that actually
  ships.
- `.login-logo`/`.selforder-logo` (login, setup, self-order screens) sit
  on `var(--surface)`, which every shipped theme sets to `#ffffff`. Only
  a *hypothetical* dark theme plugin (the "Midnight" example in the old
  CSS comment is a unit-test fixture, not a shipped theme) would make
  that background dark.
- The receipt/print logo (`internal/print/raster.go`, `Doc.Logo`) is the
  **shop's own uploaded PNG**, not the Universal Till brand mark at all —
  N/A to this card. `receipt.html`'s `@media print` also hides `.nav`
  entirely, so the till's own mark never reaches paper regardless.
- Favicon (`ut-logo.ico`), the macOS app-icon rasterization, and the
  Android launcher icons all already render the dark mark on a
  deliberate white/plate background baked into their own generation
  scripts (`packaging/macos/build-app.sh`, `android/generate-launcher-
  icons.sh`) — OS-chrome icons carry their own background tile by
  platform convention, the "decide per surface, exclusion is valid" case
  the decision comment names explicitly. Left untouched.

## Changed here

- New asset `web/public/assets/logo/unitill-logo-light.svg` — the
  canonical mark's exact path geometry with `fill:#ffffff` added to the
  wrapping `<g>`; transparent background. **Used only by `.nav`.**
- `web/ui/partials/nav.html` — img src → the light variant. `.logo`
  CSS: dropped the white plate (`background`/`border-radius`) — the
  light glyph sits directly on `.nav`'s own always-dark background.
- `scripts/ci/check-brand-assets.sh` — pins the light variant's hash
  alongside the existing canonical hash; asserts `.nav` references the
  light variant and `login.html`/`setup.html`/`self_order.html` do NOT;
  asserts the `.login-logo, .selforder-logo` CSS block (scoped via a
  small `awk`, not a whole-file grep) has no backing plate.

## Round 1 (independent review, Opus, isolated worktree)

Two blocking findings, both fixed same session, verified in a scoped
round 2 (below):

- **B1**: a *second* Playwright suite (`tests/e2e/`, run by
  `.github/workflows/ci.yml`'s `test:e2e` step — separate from `e2e/`,
  which `.github/workflows/e2e.yml` runs) had its own nav-logo test
  asserting the old filename. Missed entirely by the first draft; CI
  would have gone red. Fixed: `tests/e2e/tests/pos_ui_mvp.spec.ts`
  updated to expect the light variant.
- **B2**: the first draft put the light glyph behind a `var(--brand)`
  dark plate on `.login-logo`/`.selforder-logo` too. This didn't fix
  anything on those three surfaces (no defect existed there — `--surface`
  is white in every shipped theme) — it relocated the same "patch pasted
  on" complaint from white-on-dark to navy-on-white. Fixed: reverted
  `login.html`/`setup.html`/`self_order.html` to the canonical dark mark
  with `background: transparent` (no plate at all — the dark mark reads
  cleanly on a white card with none).

A **real bug was found and fixed while implementing the B1 guard check**:
an `-E`/`\.` grep pattern silently failed to match inside
`pos_ui_mvp.spec.ts` because that file's content is a TypeScript regex
*literal* (`/unitill-logo-light\.svg/`) — its source text contains an
actual backslash character between "light" and the dot, which `\.` (one
literal dot) doesn't account for. Every filename check in
`check-brand-assets.sh` now uses `-F` (fixed-string) matching on the bare
`unitill-logo-light` / `unitill-logo.svg` markers instead.

## Round 2 (scoped re-verification of B1 + B2 only, Opus, isolated worktree)

**PASS.** Verified live, not just by reading the diff:

- Reverted the B1 fix and re-ran `tests/e2e` — confirmed it fails exactly
  as round 1 predicted, then restored it and confirmed green.
- Real computed-style + pixel probe on `/`, `/setup`, `/self-order`:
  light glyph directly on `.nav`'s `rgb(30,41,59)`, no plate; dark mark
  directly on each card's actual background (`rgb(255,255,255)` /
  `rgb(241,245,249)`), no plate. The B2 regression is gone, not
  relocated.
- Confirmed the theme claim across all four shipped themes (`--brand`
  dark, `--surface` white, `--bg` light in every one).
- Stress-tested the new `-F` guard checks with a 9-way break matrix
  (light variant on the wrong template, plate CSS restored, hash
  tampering, CSS selector reordered, the B1 regression itself) — all 9
  caught, each restored before the next.
- Confirmed nothing else in the repo still references the light variant
  incorrectly, and the dark mark's own pin / the icon-generation scripts
  are untouched.

### Non-blocking findings (accepted, not fixed this round)

- The three new `-F`/awk guard checks assert *token presence*, not
  *absence of an override* — a second, more-specific CSS rule elsewhere
  in `app.css`, or a marker surviving only in a comment, would slip past
  them. Mitigated in practice: the e2e computed-style assertions
  (`login.spec.ts`, `self-order-brand-mark-298.spec.ts`,
  `pos_ui_mvp.spec.ts`) catch all three cases and run in CI. Accepted as
  defense-in-depth, not a gap worth a third guard-hardening pass on a
  card this size.
- The `.login-logo, .selforder-logo` CSS comment justifies both
  selectors via `--surface`, but `.selforder-logo` actually sits on
  `body.selforder-screen` (`--bg`), not `--surface`. Harmless — `--bg`
  is light in every shipped theme too — but the stated reasoning covers
  only one of the two selectors precisely. Left as a documentation nit.
- `background: transparent` on `.login-logo, .selforder-logo` is the
  initial/no-op value — it exists so the guard has a token to match, not
  because it changes rendering. Worth knowing, not worth removing (the
  guard needs *something* concrete to assert).
- `nav.html`'s guard check is positive-only (light variant present); no
  negative check that it doesn't *also* reference the dark mark, unlike
  the three-way check the other templates get. Low risk (nothing in the
  diff does this), noted for a future tightening pass rather than fixed
  now.
- The light variant isn't mirrored into `ut-docs/logo/` (that mirror
  holds only the originally-supplied master artwork, and its own guard
  checks the canonical dark hash, which is unchanged and still correct).
  Deliberately not mirrored — it's a derived, till-UI-specific recolor,
  not a new master asset.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...`, `go test ./...` — clean (no Go
  source touched by this diff at all).
- All 20 of this repo's CI guard scripts exit 0, including
  `check-brand-assets.sh`, `guard-data-access.sh`,
  `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`,
  `guard-i18n.sh`, `guard-help-topics.sh`, `guard-docs-shots.sh`.
- `make docs-shots` run to completion twice across the two review
  rounds (68/68 both times) — this is itself the first real production
  use of ut-docs#622's cloud-Chromium fix, landed earlier this same
  cycle.
- Full `e2e/` suite: 117 passed, 1 failed
  (`catalog-image-to-till.spec.ts`) — confirmed pre-existing and
  unrelated by reproducing the identical failure against clean `main`
  (both independently, in round 1 and round 2).
- Full `tests/e2e/` suite: 21 passed, 4 skipped (docs-hub specs, need a
  `DOCS_ROOT` this environment doesn't have — same as any other run
  without that secret).
- Visually inspected regenerated screenshots (`web/help/img/en/sell.png`
  and others) — clean white glyph directly on the dark header, no tile.
- No real client/shop name, no literal credential in any added line.

## Safe-to-merge verdict

Yes. Both blocking findings from round 1 are fixed and independently
re-verified live in round 2; no new blockers found. Non-blocking items
above are accepted as documented, deferred follow-ups rather than
blockers on a card this size.
