# 2026-08-08 — Disable browser autofill / field-history dropdown (ut-docs#400)

**Card:** universaltill/ut-docs#400 — field report: the browser's own
autofill/typed-value-history dropdown covers the sale screen's barcode
field mid-scan, offers wrong values from unrelated fields, and on a till
shared by several staff leaks one operator's typed history to the next.
Requirement: suppress it across every till input, centrally (not
per-template, so a new form can't silently reintroduce it), guarded
against regression.

## Design

A runtime sweep — not per-template `autocomplete=` attributes — walks
every `<input>`/`<textarea>` on the page: at load, on every `htmx:afterSwap`,
and via a `MutationObserver` for anything added afterwards by any other
means (plugin content, a future form, …). For text-entry types:

- `autocomplete` gets a unique, non-standard token (`off-<name-or-id>`)
  only if nothing was already declared. Plain `autocomplete="off"` is
  documented as unreliable on some Chromium builds (the concern behind the
  card's own "verify on the actual Pi kiosk Chromium" note); an
  unrecognized token is spec-processed as `off`, which is the
  belt-and-braces version. An element that already declares its own
  `autocomplete` — `web/ui/pages/pin.html`'s `current-password`/
  `new-password`, a deliberate password-manager integration for the
  PIN-change form — is left alone.
- `autocapitalize`/`autocorrect` are forced off unconditionally.
  `spellcheck` is forced off on `<input>` only — the one `<textarea>` in
  this codebase (the bug-report note field) is free prose the shop owner
  actually writes, so it keeps normal spellcheck, while still getting the
  autocomplete/autocapitalize/autocorrect treatment (a "previous notes"
  history dropdown is the same privacy leak as anywhere else).

## Independent review (Opus, fresh context) — found a blocker and a major gap, both fixed here

Full agent review ran against the first version of this diff. Findings and
what changed as a result:

1. **BLOCKING — CI would go red independent of this change.**
   `scripts/ci/guard-docs-shots.sh` hashes `web/public/**` as part of the
   manual's screenshot-freshness surface; editing `app.js` invalidated it.
   Fixed by running `make docs-shots` and committing the regenerated
   `web/help/img/manifest.json`. Two topics' screenshots (`alerts`,
   `designer`, all 4 locales) came back with a small, localized pixel diff
   (~0.03% of the frame, in a small text-sized region) unrelated to this
   diff — almost certainly a live clock/relative-time element re-rendered
   at a different wall-clock moment between capture runs, not a real
   regression. No other screenshot changed.

2. **MAJOR — "everywhere" wasn't everywhere.** The first version put the
   sweep inside `app.js`, which is loaded only via
   `web/ui/layouts/base.html`. Four page templates are **standalone
   documents that bypass that layout** and own their own `<script>` tags:
   `login.html`, `setup.html`, `self_order.html`, `self_order_shop.html` —
   the exact same shape of gap `ut-docs#344` hit with htmx on this same
   `setup.html` (missing script tag broke join enrolment in the field,
   which is why `guard-htmx-loaded.sh` exists). The reviewer flagged that
   `login.html` is precisely the screen every operator handover on a
   shared till shows — the sharpest instance of the card's own threat
   model — and `setup.html` has real unsuppressed `type=text` fields
   (`store_name`, `till_name`, pairing `code`/`name`) plus PIN fields.
   The original guard also only checked `base.html`, so it certified green
   while the requirement was unmet on 4 of 5 documents.

   **Fix:** extracted the sweep out of `app.js` into its own
   `web/public/autofill.js`, added its `<script>` tag to all five
   documents (`base.html` + the four standalone ones), and rewrote
   `scripts/ci/guard-autofill-suppression.sh` to scan every standalone
   document under `web/ui/**` (same `<html>`-detection idiom as
   `guard-htmx-loaded.sh`, including its fail-closed "found nothing to
   check" case), not just `base.html`.

3. **MINOR — guard false-passed on a commented-out script tag.** The first
   guard version grepped raw file text — `guard-htmx-loaded.sh`'s own
   documented regression class (a disabled script during debugging would
   have kept CI green). Fixed: both the HTML template scan and the
   `autofill.js` marker scan now strip comments first, with regression
   fixtures for both in `guard-autofill-suppression_test.sh`.

4. **MINOR — `off-<key>` could emit a multi-token `autocomplete` value.**
   The key was taken from `name`/`id` unsanitized, and at least one `name`
   is externally supplied (`plugin_settings.html`'s
   `name="setting_{{ .Key }}"`, a plugin-declared key). A key containing a
   space would produce two tokens instead of one. Not a security issue
   (never parsed as markup, only ever reaches `setAttribute`), but fixed
   for correctness: the key is now sanitized to `[A-Za-z0-9_-]` before use.

5. **MINOR — `spellcheck="false"` on the one free-prose textarea.**
   Addressed above in the design section — `<textarea>` keeps its default
   spellcheck now.

All five fixed in this same commit. Re-verified after fixing: all guards
pass (`guard-data-access`, `guard-kiosk-engine`, `guard-i18n`,
`guard-htmx-loaded`, `guard-autofill-suppression` + its self-test,
`guard-docs-shots`), `go build ./...` / `go vet ./...` clean, full
`go test ./...` green.

## Verified beyond automated tests

- `e2e/tests/autofill-suppression-400.spec.ts` (real Chromium): the
  reported barcode/qty fields get `autocomplete` matching `/^off-/` plus
  the other attributes; an id-only field gets the id-based fallback key;
  an explicitly-declared `autocomplete` (the pfand modal's `manager_pin`)
  is left untouched while still getting the other three; a field added
  later via an htmx swap (the basket's `.qty-input`) is caught too.
- `e2e/tests/login.spec.ts` extended: the setup wizard's `store_name` and
  `pin` fields — on `setup.html`, the exact standalone document the
  review's major finding was about — get `autocomplete` matching
  `/^off-/` in a real first-boot flow, not a synthetic fixture.
- Full e2e suite run twice (before and after the fix round): 115/116
  passing both times; the one failure
  (`catalog-image-to-till.spec.ts`'s image-load timing) reproduces
  identically on unmodified `main` with this branch's changes stashed
  out — confirmed pre-existing and unrelated, not a regression from this
  change.
- `make docs-shots` run for real against a live till; manifest and 8 PNG
  diffs inspected pixel-by-pixel (see finding 1 above) before committing.

## Guarded against regression, two ways deliberately different in kind

- `scripts/ci/guard-autofill-suppression.sh` (+ self-test, 9 fixture
  assertions): a cheap static check that every standalone document loads
  `autofill.js` and that `autofill.js` still contains the sweep's own
  machinery — `go test` never renders a page, so nothing else would
  notice either being silently deleted.
- `e2e/tests/autofill-suppression-400.spec.ts` +
  `e2e/tests/login.spec.ts`: real-Chromium behavioral coverage.

## Manual / README

Not updated. This only suppresses a browser annoyance — it adds, removes,
or alters nothing a shop owner sees or does, so the "manual ships with the
feature" rule (aimed at real UI/workflow changes) doesn't apply.

Closes universaltill/ut-docs#400
