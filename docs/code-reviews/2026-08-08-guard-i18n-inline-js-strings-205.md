# Code review: guard-i18n.sh doesn't scan inline `<script>` string literals (ut-docs#205)

**Date:** 2026-08-08
**Card:** universaltill/ut-docs#205
**Author (Dev):** Sonnet, inline (this cycle's Scrum Master)
**Reviewer:** Opus, independent fresh-context subagent, worktree-isolated

## What shipped

- New check 5 in `scripts/ci/guard-i18n.sh`: flags a hardcoded prose string
  literal assigned to `.textContent`/`.innerHTML` inside an inline
  `<script>` block anywhere under `web/ui/**/*.html`. Regex captures the
  assigned literal; HTML tags are stripped from it (`<[^>]*>` → space)
  *before* the prose heuristic runs, so a markup skeleton being assembled
  in JS (e.g. `innerHTML = '<ul style="list-style:none; ...">'`) doesn't
  false-positive on attribute-name pairs like "ul style", while a real
  prose string wrapped in a tag (`'<p class="muted">No matches.</p>'`)
  still correctly flags. Same `i18n:ignore` same-line exemption as the
  existing Go-side check.
- New `scripts/ci/guard-i18n_test.sh` — 6 regression cases (real
  prose reject, `i18n:ignore` exempt, markup-only false-positive check,
  tag-wrapped prose still caught, single-word heuristic-gap accepted,
  real-tree sanity pass), mirroring `guard-kiosk-engine_test.sh`'s
  plant/expect_fail/expect_pass pattern. Wired into
  `.github/workflows/ci.yml` immediately after the `guard-i18n.sh` step.
- `web/ui/pages/settings.html`: `data-reset-btn` and `export-run-btn`
  handlers migrated from hardcoded English literals to a
  template-populated `var T = {...}` JS lookup, following the precedent
  in `web/ui/partials/bugreport_panel.html`. 5 new locale keys added to
  all 4 locale files (`en`/`ar`/`fa`/`tr`):
  `settings.data.reset_confirm_required`, `settings.data.reset_clearing`,
  `settings.data.reset_confirm_dialog`,
  `settings.data.export.pick_dates_required`,
  `settings.data.export.downloaded`.
- 10 pre-existing, out-of-scope violations of the new check tagged
  `// i18n:ignore` (not migrated this cycle, deliberately deferred):
  `web/ui/layouts/base.html:97,102,114,145` (self-update button status
  text), `web/ui/pages/catalog.html:276,277,288` (item-image upload
  messages), `web/ui/pages/settings.html:282,322,330` (the other
  data-management handlers' status/confirm messages).
- `CLAUDE.md` gets a new bullet documenting that the `{{ T }}` rule
  extends to inline `<script>` status text, the `var T = {...}` pattern
  to use, and the `i18n:ignore` escape hatch — plus the known
  `web/public/` coverage gap (below).
- `web/help/img/manifest.json` + regenerated screenshots (60 PNGs across
  15 topics × 4 locales) — required by `guard-docs-shots.sh`, whose
  surface hash covers all of `web/ui/**` regardless of whether a given
  edit is visually significant. Only `alerts` and `designer` came out
  pixel-different from before, and only because those two topics render
  the current wall-clock time/date into the screenshot — unrelated to
  this diff; verified by eye, both look correct.

## What the independent review found

Spawned as an Opus subagent in an isolated git worktree (branched from a
`WIP: pre-review snapshot` commit on `fix/205-guard-i18n-inline-js-strings`),
briefed with the full diff scope and told to actually run things, not just
read.

**1 blocking issue, found and fixed:**
- `scripts/ci/guard-docs-shots.sh` failed — the diff's edits (including the
  comment-only `i18n:ignore` additions) changed `web/ui/**` file bytes,
  drifting the manual's surface hash. Fixed by regenerating the screenshots
  and manifest via `make docs-shots`'s underlying Playwright suite
  (`playwright.docs.config.ts`, run against the environment's pre-installed
  Chromium — no config change committed) and re-running the full gate
  afterward. Re-verified independently: reverting just `web/ui`,
  `web/locales`, `internal/pages` to the pre-diff commit makes the guard
  pass again, confirming the failure was caused by this diff and not
  pre-existing.

**2 medium issues, both fixed:**
- The `data-reset-btn` handler's native `confirm()` dialog — the longest,
  most consequential string in that flow (a destructive-action warning) —
  was left hardcoded English even though it sits inside the handler this
  card was already migrating. Not caught by the guard (`confirm()` isn't a
  scanned sink) and not tagged `i18n:ignore`, so it would have shipped
  silently. Fixed: moved into the same `var T` lookup
  (`settings.data.reset_confirm_dialog`), translated into all 4 locales.
- `web/public/app.js` carries the same class of hardcoded-string bug
  (`innerHTML = '<p>No pending payments yet.</p>'` and a similar sibling)
  but sits outside the new check's `web/ui/**/*.html` glob entirely, so
  nothing in this diff catches it. Widening the glob to cover shipped JS
  under `web/public/` is real scope beyond this card (a different file
  type, a different risk profile for false positives over real JS logic,
  not just markup templates) — recorded explicitly in the guard's own
  header comment and `CLAUDE.md` as a known gap, and filed as a follow-up
  Backlog card (ut-docs#<followup>) alongside the already-planned
  repo-wide inline-JS migration.

**Low/nit findings — accepted as-is, explained below:**
- `guard-i18n_test.sh`'s `clear_fixture()` wipes the whole `fixtures`
  array rather than removing one entry — a latent cleanup-ordering trap if
  a future case ever plants two fixtures at once. Identical shape already
  exists in the precedent file, `guard-kiosk-engine_test.sh`, and no case
  in either file plants more than one fixture at a time. Left matching the
  established repo pattern rather than diverging in only the new file;
  noted here rather than silently ignored.
- The header comment in `guard-i18n.sh` (checks 1-3 enumerated) — fixed:
  now documents checks 4 and 5 too.
- Placeholder-free string concatenation (`'✓ ' + T.downloaded + ' ' +
  filename`) reads awkwardly in some locales and is bidi-unfriendly in
  ar/fa. Not actionable today — the `T` template func is `func(string)
  string`, no placeholder/interpolation support exists in this codebase
  yet. Noted, not fixed; unchanged from the pre-existing pattern used
  everywhere else in this file (`'✓ ' + (message)`).
- Locale-key insertion order (`export.downloaded` lands slightly
  out-of-alphabetical-order relative to its neighbors) — the locale files
  already have well over 100 out-of-order pairs; no real convention is
  broken.

## Independently re-verified (by the reviewer, not taken on trust)

- **Revert-then-restore TDD check, done for real**: reverted only
  `scripts/ci/guard-i18n.sh`'s new check back out, re-ran
  `guard-i18n_test.sh` — the two `expect_fail` cases genuinely failed
  (`expected guard to reject … but it passed`), proving the regression
  test actually depends on the fix. Restored the fix, all 6 cases passed
  again.
- Adversarial read of the regex/heuristic: checked for false negatives
  (backtick template literals, `.innerText`/`setAttribute`/`alert`,
  multi-line assignments, single-word status text) and false positives
  (CSS class names, data-attribute values, a JS `<`/`>` comparison being
  mistaken for a tag) beyond the 6 test cases. Found the two medium issues
  above (both real, both fixed); everything else either doesn't occur
  live in this tree today or is the same accepted recall/precision
  tradeoff the pre-existing Go-side check already documents.
- **JS-context escaping proved safe, not assumed**: `T` returns a plain
  `string` (not `template.HTML`), rendered through `html/template`'s
  contextual auto-escaper. Confirmed with a hostile translation value
  (`"); alert(1)…`) that the escaper correctly neutralizes it inside the
  `var T = {...}` JS-string context. This matters because translation
  values are manager-editable via the in-product Translation editor.
- End-to-end render verified against a real running till (not just
  template-parse): `GET /settings` in both `en` and `fa` (`?lang=fa`)
  emits the real localized strings with no `{{ T … }}` leaking through
  and `dir="rtl"` correctly set for `fa`.
- Locale files: all 4 valid JSON, identical key sets, no English-copy-paste
  into a non-English locale.
- `go build ./...`, `go vet ./...`, full `go test ./...` (all packages),
  `guard-data-access.sh`, `guard-kiosk-engine.sh`, `guard-help-topics.sh`
  all re-run clean after every fix, not just once.

## Verified beyond automated tests (Tester step, before Reviewer)

- Real running throwaway till (`UT_AUTH=off`), driven with a real headless
  Chromium session (Playwright, ad hoc — not the checked-in e2e suite):
  clicked both migrated handlers' trigger paths in **English** and
  **Farsi/RTL**, confirmed the correct localized text appears live in the
  DOM, `dir="rtl"` is set, zero console/page errors in either locale.
  Full-page screenshots taken and read by eye — no layout regression in
  either locale (RTL mirrors correctly; the confirm/status messages fit
  their row without overflow or overlap).
- `export-run-btn`'s guarded-by-`{{ if .exportEntries }}` behavior
  confirmed correct on a fresh till with no export plugin installed (the
  button legitimately doesn't render — pre-existing, unrelated logic;
  correctly not exercised).

## No help-manual prose update needed

No `web/help/` topic exists for `/settings`, no topic mentions any of the
changed strings, and no route/screen/behavior changed — only wording that
was English-only is now localized. `guard-help-topics.sh` passes. The
manual's *screenshots*, however, did need regenerating — covered under
the blocking fix above, since the screenshot-freshness guard's surface
hash isn't scoped to "visually significant" changes.

## No client/shop-name or secret-shaped literal

Checked explicitly — none in this diff.

## Safe-to-merge verdict

**Yes**, after the blocking fix (screenshots regenerated) and the 2 medium
fixes (confirm-dialog localized, `web/public/` gap recorded + carded) were
applied. Full gate green: build, vet, full test suite, all 6 CI guards
(`guard-data-access.sh`, `guard-kiosk-engine.sh`, `guard-i18n.sh`,
`guard-i18n_test.sh`, `guard-docs-shots.sh`, `guard-help-topics.sh`).

## Explicitly deferred (new Backlog card to be filed)

- Migrate the 10 `i18n:ignore`-tagged pre-existing violations
  (`base.html`'s self-updater, `catalog.html`'s image-upload messages,
  `settings.html`'s other data-management handlers).
- Widen (or explicitly scope) coverage to shipped JS under `web/public/`
  (confirmed live instance: `app.js`'s pending-payments message).
