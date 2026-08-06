# 2026-08-06 — user manual two-pane shell (ut-docs#325)

## What shipped

Replaced the flat `/help` accordion (16 hardcoded feature ids, all locale
strings in `web/locales/*.json`, no search, no deep links) with a two-pane
manual: a left topic tree (sections → topics, plus a search box) and a right
panel rendering the selected topic. This is the **frame**, not the full
illustrated manual — content, screenshots, contextual `?` links and the
staleness CI guard are separate cards (#326, #327) per the epic (#324).

- New package `internal/help` (loader/lookup/search) — topics are Markdown
  files with YAML front matter under `web/help/<lang>/*.md`, `go:embed`ed
  (`web/embed.go`), parsed once at startup with `gopkg.in/yaml.v3` +
  rendered with `goldmark` (default safe renderer — no `html.WithUnsafe()`).
  English is authoritative; a locale missing a topic falls back to English
  with a visible "not yet translated" banner.
- `internal/pages/help_page.go` rewritten: `GET /help` (full shell),
  `GET /help/{topic}` (htmx fragment or full page depending on `HX-Request`),
  `GET /help/search` (server-side, in-memory, ranked substring match).
- Content migration: all 16 existing `help.feat.*` topics + a folded-in
  `quick-start.md` (the old `help.guide.*` quicksteps + plugins-FAQ blurb) —
  17 topics total, byte-identical prose to what they replaced (verified, see
  below).
- New Playwright spec `e2e/tests/help-manual.spec.ts`: two-pane layout +
  htmx tree navigation + URL push, search-without-reload, direct-link
  200/404, and RTL layout flip under `fa` (bounding-box assertion, not a
  screenshot diff).
- `internal/pages/index_page.go`: removed a dead `/help` special-case branch
  in the `/` catch-all (see finding below).

## TDD

Dev implemented test-first (Go tests + the Playwright RTL spec both
confirmed red before the fix, green after). Independently re-verified in
review via mutation testing — see below, not taken on the report's word.

## Independent review (fresh-context Opus subagent, hard-complexity card)

**Verdict: safe to merge with should-fix items** (no blocker-class —
money/tax/data-loss/security — issues). Re-ran the full gate personally
(`go build`, `go vet`, both guards, `go test ./... -race`, the full
Playwright suite in `e2e/`), re-verified TDD via mutation testing (flipped
each real assertion's underlying behavior — search ranking, front-matter
validation, English fallback, htmx-vs-full-page branch, RTL CSS, and a new
goldmark-safety test — confirmed each went red for the right reason then
green again), mechanically diffed all 17 migrated topics against the locale
strings they replaced (byte-identical, zero drops), and confirmed the
"dead code" claim on `index_page.go` by proving `ServeMux` pattern
specificity independently.

**Two real bugs found and fixed in review, both re-verified with the full
gate + full e2e suite (51/51) after fixing:**

- **CI break**: removing the old accordion also removed the
  `data-testid="plugin-faq-entry"` link, which the **other**, CI-run e2e
  suite (`tests/e2e/tests/pos_ui_mvp.spec.ts`,
  `tests/e2e/tests/plugin_install_flow.spec.ts` — separate from this
  card's `e2e/`) asserts is reachable from `/help`. CI's seeder installs no
  plugin, so the real FAQ plugin entrypoint doesn't exist in that
  environment either — the static link had to come back, not be
  redirected. Restored verbatim into the new shell's nav aside
  (`web/ui/pages/help.html`).
- **RTL bidi bug**: the English-fallback topic body, rendered inside a
  `dir="rtl"` document (any `fa`/`ar`/`tr` request today, since no topic
  translations exist yet), had its sentence-final punctuation and
  ordered-list markers thrown to the wrong side by bidi reordering — a real,
  visible defect across the entire non-English manual experience until
  ut-docs#341 lands. Fixed with `lang`/`dir="ltr"` scoped to just the title
  + body (`web/ui/partials/help_topic.html`) — page chrome and the banner
  itself stay in the reader's direction.

**Should-fix items, not blockers, filed as new Backlog cards rather than
chased in this diff** (both are genuinely separate follow-up scope, not
omissions in this card):

- Stale tree active-highlight / `aria-current` after an htmx topic swap —
  actively misinforms screen readers; the fix (an `hx-swap-oob` tree update)
  has a real design tradeoff against an active search filter, so it's a
  Dev/Architect call, not a mechanical fix.
- ~100 now-unused `help.feat.*`/`help.guide.*` locale keys left in all four
  locale files — harmless (guard-i18n only checks used-key resolution and
  locale parity, not dead keys) but pollutes the `/translations` editor.

**Nits** (not filed, low value): `search` is a reserved topic id (a future
`web/help/en/search.md` would be unreachable — worth a code comment);
`TestSearchCapsResults` asserts `>20` rather than `==20`; search stops
falling back to English once a locale has *any* topics of its own
(irrelevant until ut-docs#341); topic `##` headings render at the same
`<h2>` level as the topic title.

**Also checked and clean**: no raw SQL anywhere in this diff (guard
confirms); zero disk writes in `internal/help`/`help_page.go` — no
`MkdirAll`/cwd-relative-path risk (both repeat-offender bug classes are
structurally N/A, package is embed-only); search is plain
`strings.Contains`, no regex/backtracking/reflected-input risk; Markdown
source files are not statically served (`/help/en/sell.md` → 404); zero
literal `left`/`right` in the new CSS, logical properties throughout; no
secrets or real client/shop names in the 17 topic files; `-race` clean
(index is immutable-after-load).

Full review transcript retained in the pipeline session; this record is the
close-out artifact.

## Verified beyond automated tests

- Real driven run (both phases — Tester and Reviewer, independently):
  screenshots of `/help` in `en` (LTR, default theme), `/help?lang=fa`
  (RTL, tree flips to the inline-end side, untranslated banner visible),
  a direct topic link (`/help/backups`, pre-selected in the tree), and a
  480px narrow/kiosk viewport (tree stacks above the topic panel per the
  media query). One apparent status-bar overlap in a full-page screenshot
  was confirmed to be a Playwright fullPage-compositing artifact with a
  `position: sticky` element, not a real bug (re-shot with a real scrolled
  viewport — no overlap).
- The RTL bidi bug (F2 above) was caught by *actually reading* the
  screenshot, not just the passing bounding-box assertion — the geometry
  test proved the columns flip; it doesn't read the prose.

## Safe to merge

Yes — no money/tax/data-loss/security-class issues; both real defects found
were fixed in the same review round and re-verified with the full gate.
