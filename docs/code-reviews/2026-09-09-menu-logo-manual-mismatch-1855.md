# Code review: fix stale "tap the logo" manual instruction (ut-docs#1855)

**Date:** 2026-09-09
**Card:** ut-docs#1855 — "Manual says \"tap the logo to get back to selling\"
but the logo isn't clickable"
**Branch:** `fix/1855-menu-logo-manual-mismatch`
**Diff:** `web/help/{en,tr,de,ar,fa}/menu.md`, `web/help/img/manifest.json`
**Reviewer:** independent (fresh-context Sonnet subagent, did not write the
change — `complexity:easy`, per the model-routing table this relaxes
"different model" to "different instance")

## What shipped

`web/help/*/menu.md` told the operator to "tap the logo to get back to
selling." `web/ui/partials/nav.html`'s logo is a bare `<div class="logo">`
with no `<a>` wrapper, no `href`, no click handler anywhere in the CSS/JS —
confirmed not clickable in the current UI. The instruction predates
ut-docs#1829 (Home/Start tile removal) and was stale copy from an earlier
UI iteration.

Docs-only fix: replaced the false claim in all five shipped locales
(en/tr/de/ar/fa) with the two affordances that actually exist on `main`
today:
- the Menu page's own `← Back to sale` button (`web/ui/pages/menu.html`,
  locale key `menu.back_to_sale`), and
- the nav rail's `Till` link (`web/ui/partials/nav.html`,
  `data-testid="nav-till"`, `href="/"`, locale key `nav.till`, rendered on
  every page via `web/ui/layouts/base.html`).

No behavior change — restoring logo-click-to-sell was considered and
rejected: the nav rail (added 2026-08-30, ut-docs#1332) already gives every
page a dedicated way back, so reintroducing a second, undocumented
navigation affordance on the logo would be a product decision, not a docs
fix, and isn't what this card asked for.

**Cross-repo note for the German string:** `de` is not a core-shipped
locale in `web/locales/` — its UI strings live in the `ut-plugin-language-de`
plugin repo (`locales/de.json`). Quoted `menu.back_to_sale`/`nav.till`
directly from that repo's file rather than guessing a translation.

## Independent review

Reviewer verified (not just read) all of the following:
- Both cited UI affordances actually exist with the wording used — read
  `menu.html`/`nav.html` directly, confirmed `nav.html` renders on every
  page via `base.html`.
- Every quoted translated label (en/tr/ar/fa from `web/locales/*.json`, de
  from the sibling plugin repo's `locales/de.json`) matches the real
  shipped string for both `menu.back_to_sale` and `nav.till`.
- Diff is prose-only in the five `menu.md` files plus the manifest hash
  update — no unrelated edits, markdown bold markers balanced,
  frontmatter untouched.
- `scripts/ci/guard-help-topics.sh` passes.
- No real client/shop name or secret-shaped value introduced.

No findings. Verdict: safe to merge as-is.

## Verified beyond the subagent's pass

- `bash scripts/ci/guard-help-topics.sh` — clean (route conflicts, topic
  parsing, shipped-locale completeness, route↔topic claim).
- `bash scripts/ci/guard-docs-shots.sh` — failed before `make docs-shots`
  (topic markdown changed since its screenshot content-hash was taken, for
  en/fa/ar/tr — `de` wasn't flagged since it isn't in the core manifest at
  all); ran `make docs-shots`, which re-ran the full Playwright docs-shot
  suite. Every locale's screenshot PNG came back byte-different (font/AA
  rendering noise from re-running the suite, the same noise class already
  documented in `docs/code-reviews/2026-09-09-xlsx-merge-scope-1853.md`'s
  precedent) — reverted every PNG back to `main`'s version and kept only
  `web/help/img/manifest.json`'s four changed content hashes (en/fa/ar/tr
  `menu` entries), matching that precedent's handling exactly. Re-ran the
  guard clean afterward: `27 routed topics × 4 locales screenshotted and
  fresh`.
- No TDD claim to re-verify — this diff has no code/test change to
  revert-and-restore.

## Not in scope

- Restoring an actual logo-click affordance (a product/UX decision, not a
  docs bug) — the card explicitly offered this as one of two options and
  the docs-fix path was chosen; flagged here for anyone revisiting the
  logo's behavior later.
