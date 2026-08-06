# Review — user manual shell (`/help` rebuilt as a searchable two-pane manual)

Ticket: universaltill/ut-docs#325 (epic ut-docs#324)
Date: 2026-08-06

## What shipped

The old `/help` was a flat `<details>` accordion over 16 feature ids hardcoded
in Go, with its content living as `help.feat.<id>.s1/.s2/.s3` locale keys. It
had no navigation, no search, no images, and no way to link to a specific
topic. This replaces it with the frame the illustrated manual (ut-docs#324)
hangs off:

- `internal/manual` — topics are Markdown files under
  `web/help/<locale>/<id>.md` with a small front-matter header
  (`id/title/section/order/summary/routes/keywords`), embedded via `go:embed`
  so the manual works with the network off. The tree is derived from the files;
  adding a topic is adding a file.
- `/help` (index), `/help/{topic}`, `/help/search?q=` — a direct hit renders the
  whole two-pane page so a topic URL is shareable; an htmx request renders just
  the reading panel.
- Contextual `?` in the nav, resolving through each topic's declared `routes:`
  to the topic documenting the page being viewed, falling back to the manual's
  index for a page nothing claims yet.
- 17 topics × 4 locales migrated from the old locale keys — including the
  quick-start cards, which would otherwise have been orphaned. fa/ar/tr were
  already translated; that prose was carried across rather than re-authored.

Deliberately **not** in scope: screenshots (ut-docs#327 builds the harness),
the CI route guard (ut-docs#326), and the deeper per-area content
(ut-docs#328–339). The topics are currently the migrated text, not the finished
book.

## What the review found

Both findings came from **driving the real page in a browser**, not from
reading the diff — the Go tests were green through both.

1. **Every English topic claimed to be untranslated.** The till's default
   locale is a BCP-47 tag (`en-US`, from `UT_DEFAULT_LOCALE`), while the topic
   directories are bare language codes (`en`). The exact-match lookup missed,
   fell back to English, and stamped "This topic hasn't been translated yet"
   across the entire English manual. Fixed by trying the base subtag before
   falling back, and by not flagging English-for-English as a fallback.
   Regression test: `TestTopicMatchesRegionalLocaleTag`, plus an e2e assertion.

2. **The `?` resolved correctly on `/` and silently degraded everywhere else.**
   `helpHref` was bound per-request in `httpx.Render` only; most pages render
   through `httpx.RenderWith`, which kept the static `/help` default. A `?`
   that always lands on the contents page looks like it works. Fixed by
   binding it in one shared helper used by both paths. Regression test:
   `TestHelpHintResolvesPerPage` (drives `/catalog`, the RenderWith path).

Two smaller fixes, same origin: the search box never fired (htmx `changed` was
declared on the `<form>`, which has no value to track — moved to the input),
and search snippets rendered in flat lower case (the lower-cased copy is now
kept for matching only).

## Verified beyond automated tests

- Driven live against a seeded till (`e2e/run-till.sh`): index, topic
  navigation, direct topic URL, search, no-results, 404 on an unknown topic.
- `?` checked on every reachable page: `/`, `/catalog`, `/inventory`,
  `/reports`, `/plugins`, `/settings`, `/journal`, `/shifts`, `/tills`,
  `/designer`, `/receipt-designer`, `/audit`, `/import` all resolve to a topic.
  `/users`, `/locations` are auth-gated (403) and `/invoices` redirects — not
  manual concerns.
- RTL confirmed by screenshot in `fa`: the tree mirrors to the inline-end side,
  content is Persian, no fallback notice. Asserted in the e2e spec by comparing
  the two panes' bounding boxes rather than by eye.
- Full gate green: `go build`, `go vet`, `go test ./...`, `guard-i18n.sh`
  (826 keys, all four locales matching), `guard-data-access.sh`.
- New Playwright spec `e2e/tests/manual.spec.ts` — 9 tests, all passing.

## Not verified / deferred

- No screenshots yet, so the image styling (`.manual-topic img`) is untested
  against real content — ut-docs#327.
- No CI guard yet asserting every route has a topic; today's coverage is the Go
  test over the routes that do — ut-docs#326.
- Not driven on real Raspberry Pi hardware at 1024×600; the layout collapses to
  one column under 52rem by design, but that is a desktop-browser observation.

## Verdict

Safe to merge. It is a strict improvement on what `/help` was, no existing
content was lost, and the two defects the driven run surfaced are fixed with
regression tests rather than noted for later.
