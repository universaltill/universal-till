# Code review — ut-docs#898: `.users-inline input` rename-field clipping

**Date:** 2026-08-23
**Card:** [ut-docs#898](https://github.com/universaltill/ut-docs/issues/898)
**Complexity:** easy
**Build model:** Sonnet (inline) — **Review model:** Sonnet, fresh-context subagent (per the easy-tier routing: an independent instance is the "different model" for this tier)

## What shipped

`web/public/app.css`'s `.users-inline input { width: 7.5rem; }` is shared by
every admin page built on the list+rename pattern. On `web/ui/pages/
locations.html` and `web/ui/pages/registers.html`, the per-row "rename"
text input (`<input type="text" name="name" value="{{ .Name }}">`) clipped
names longer than ~13 characters mid-word with no ellipsis — reproduced
live with the seeded `Default Register` name (17 chars), rendering as
`Default Regis` cut off flush against the input's edge, in both LTR and
RTL.

Fix: a new, more specific `.users-inline input.rename-input` rule
(`width: 13rem; text-overflow: ellipsis; overflow: hidden;`), applied only
to the one rename input on `locations.html` and `registers.html` via
`class="rename-input"`. The base `.users-inline input` rule (7.5rem, no
ellipsis) is untouched, so every other page sharing `.users-inline`
(`kitchen_stations.html`, `promotions.html`, `country_settings.html`,
`tables.html`, and `users.html`'s own PIN/role/active forms) renders
exactly as before — verified by grep and by re-diffing every regenerated
manual screenshot against the pre-change baseline (see "What was verified"
below).

`/users` itself needs no change: it has no per-row free-text "name" rename
input using `.users-inline` today (only a PIN password field, a hidden
active-toggle, a role select+button, and a promote button) — the issue's
mention of `/users` names the shared class's origin page, not a second bug
site.

## What the independent review found

Spawned a fresh-context Sonnet subagent (general-purpose) with the exact
diff, told to run build/vet/tests/guards itself and find real problems,
not confirm the work. Verdict: **safe to merge, no blocking issues.**
Four non-blocking observations, three fixed here, one deferred:

1. **Fixed.** `max-width: 16rem` was dead code — `width: 13rem` is fixed
   with nothing that ever relaxes it, so `max-width` could never bind.
   Removed; the rule is now just `width: 13rem` (no min/max-width — this
   field never needs to shrink below or grow past that).
2. **Fixed.** The CSS comment's list of unaffected `.users-inline`
   consumers omitted `country_settings.html`'s checkbox. Comment corrected
   to name it.
3. **Tried, found to regress, reverted.** The reviewer flagged (as
   "PLAUSIBLE, not confirmed — no browser available") that `.users-inline`
   has no `flex-wrap`, so the wider 13rem input could in principle overflow
   its row on a narrow viewport with no scroll fallback. I added
   `flex-wrap: wrap` to the base `.users-inline` rule as a defensive fix
   and re-ran `make docs-shots` to check — this is exactly what caught the
   regression: `/users`' role-select + "Change role" button (one
   `.users-inline` form, `users.html:97-108`) now wrapped onto two lines
   at full desktop width, not just narrow viewports, because `flex-wrap`
   changes a flex container's *intrinsic-sizing* algorithm, not just its
   overflow behaviour. Confirmed with a byte-level diff of
   `web/help/img/en/users.png` before/after. **Reverted** — the base
   `.users-inline` rule is untouched. Verified with a standalone
   Playwright screenshot at a 375px viewport (see below) that the
   `.rename-input`-only fix (no flex-wrap) still degrades acceptably there
   — the input+button pair for a rename form doesn't overflow at that
   width even without an explicit wrap rule, because 13rem (208px) +
   button + gap fits inside a 375px viewport's available card width in
   practice. A genuinely narrower/embedded-webview case is still a real,
   if now unconfirmed-either-way, open question — noted below rather than
   forced through with a change that had a confirmed side effect.
4. **Deferred — not this card.** Neither `locations.html` nor
   `registers.html` is ever actually screenshotted by the manual: the
   docs-shots harness only visits each topic's `routes[0]`, and both pages
   are covered by topics whose `routes[0]` is a different page
   (`inventory` → `/inventory`, claims `/locations`; `multitill` →
   `/tills`, claims `/registers`). Pre-existing tooling gap, unrelated to
   this fix — filed as ut-docs#900.

Also flagged and independently confirmed during this fix: `registerLocations`/
`registerRegisters` (`internal/pages/locations_page.go`,
`internal/pages/registers_page.go`) still gate on the old flat
`auth.FromContext(...).IsManager()` check, never migrated to the modern
`canPerform()` gate (`internal/pages/authz.go`) that every other admin page
migrated to across #555's five successor cards (#713 was the last, #721
removed the old gate). `canPerform()` has the `UT_AUTH=off` dev/CI bypass;
the old gate does not — so with `UT_AUTH=off` (what the e2e suite's
`default` Playwright project boots with), `/locations` and `/registers`
are **permanently 403**, unreachable by any e2e spec, which is also why no
e2e coverage exists for either page today. Out of scope for a CSS fix —
filed as ut-docs#901.

## What was verified beyond automated tests

- **Visual, real-browser verification** (Playwright + the pre-installed
  Chromium, `/opt/pw-browsers`) against the actual `app.css` +
  `web/ui/pages/registers.html` markup, served statically:
  - **Before/after, LTR:** `Default Register` clips to `Default Regis`
    with the base rule; renders fully with `.rename-input`.
  - **After, pathological length:** a 53-char name truncates with a
    trailing ellipsis (`A Very Long Register N…`) instead of clipping
    flush with no indication.
  - **After, RTL (Farsi):** a long Persian name truncates with the
    ellipsis on the correct (leading, right) side — no logical/physical
    property issue, since the fix only uses `width`/`text-overflow`/
    `overflow`, none of which are direction-sensitive.
  - **375px viewport:** the `.rename-input` + button pair does not
    overflow its card.
- `gofmt -l .` — clean. `go build ./...`, `go vet ./...` — clean.
- `go test ./internal/pages/...` — pass (`internal/pages`,
  `internal/pages/catalog`, `internal/pages/common`).
- `go test ./...` (full suite) — pass, no regressions anywhere else.
- CI-blocking guards run locally: `guard-i18n.sh`, `guard-data-access.sh`,
  `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`,
  `guard-compliance-claims.sh`, `guard-docs-shots.sh` (after `make
  docs-shots` regenerated the 21-topic × 4-locale manual screenshot set —
  required since `app.css` is part of every topic's surface hash),
  `guard-help-topics.sh`, `guard-webkit-version.sh`,
  `guard-kiosk-launch-flags.sh`, `guard-android-status-address.sh`,
  `guard-android-i18n.sh`, `guard-emoji-font.sh`, `guard-htmx-loaded.sh`,
  `guard-autofill-suppression.sh`, `check-brand-assets.sh`,
  `guard-makefile-version.sh` — all pass.
- No real client/shop name or secret-shaped literal anywhere in the diff
  (pure CSS/HTML class + width/ellipsis change).

## Verdict

**Safe to merge.** No blocking findings. The one attempted extra hardening
(flex-wrap) was tried, found to regress an unrelated page, and reverted —
recorded here rather than silently dropped, since the next person touching
`.users-inline` should know why it isn't there.

## Explicitly deferred (new Backlog cards filed)

- **ut-docs#900** — `locations`/`registers` are never actually
  screenshotted by the manual (topic `routes[0]` points elsewhere).
- **ut-docs#901** — `locations_page.go`/`registers_page.go` never migrated
  off the old flat manager gate onto `canPerform()`, so both pages are
  unreachable under `UT_AUTH=off` and have no e2e coverage as a result.
