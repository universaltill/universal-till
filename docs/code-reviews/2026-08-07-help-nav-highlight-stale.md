# Code review: /help tree active-highlight goes stale after an htmx topic swap (ut-docs#351)

**Date:** 2026-08-07
**Author (Dev):** scrum-master pipeline, Sonnet
**Reviewer:** independent Opus subagent (fresh context, different model)
**Card:** universaltill/ut-docs#351

## What shipped

`web/ui/partials/help_nav.html` (new), `internal/ui/help_nav.go` (new),
`web/ui/pages/help.html`, `internal/httpx/httpx.go`,
`internal/pages/help_page.go`, plus a Go test and a Playwright test. No SQL,
no money, no new i18n keys, no file I/O.

The `/help` manual's two-pane layout swaps only `#manual-panel` on an htmx
topic click — the tree (`is-current`/`aria-current="page"`) was never
re-rendered, so it stayed pinned to whatever was correct at the last full
page load. Clicking any topic via htmx (the tree, or a search result — both
route through the same `GET /help/{topic}` handler) left the highlight wrong.

Fix: extracted the tree markup into its own partial (`help_nav.html`,
`{{ define "help_nav" }}`, `id="manual-tree"`), reused by the full-page
render and, with `OOB: true`, sent again as an `hx-swap-oob="true"`
fragment alongside the topic panel on every htmx `/help/{topic}` response —
the same out-of-band pattern already used for the sale journal
(`internal/ui/journal.go` / `pos_api.go`'s `JournalView{OOB: true}`).
`internal/ui/help_nav.go`'s `HelpNavView` mirrors `JournalView` directly.

## Independent review — findings

**Verdict: safe to merge.** No blocking findings. The independent review
(fresh-context Opus) verified both new tests fail on reverted code and pass
restored — for the Go test *and*, separately, the Playwright test, since a
browser-level false-pass is exactly where it would hide. It also probed
`$.currentID` scoping, OOB-id uniqueness (exactly one `id="manual-tree"` per
response, no duplication), same-stream write ordering, locale propagation
(en/fa/es, confirmed genuinely Persian tree content in the OOB fragment),
and the `/help` index + htmx edge case (currentID nil, no template error) —
all empirically, not by reading alone.

Six non-blocking findings, all addressed in the same round (no second
review round needed — none were blocker-class per this pipeline's
one-review-round-by-default rule):

1. **Dead `err == nil` guard + inaccurate comment**
   (`help_page.go`, at the time of review). `ui.NewHelpNavView` uses
   `template.Must`, which panics rather than returning a non-nil error, so
   the guard could never actually skip — and the original comment claimed a
   "best-effort" fallback that can't happen. **Fixed**: rewrote the comment
   to describe what the code actually does (buffer-first write, see #2)
   instead of a nonexistent construction-failure fallback. Left
   `NewHelpNavView`'s `error` return itself alone — trimming it is a
   two-line cross-cutting tidy-up shared with the pre-existing
   `NewJournalView` (same dead-error pattern there too), better done as its
   own change than smuggled into this bug fix. Noted as deferred below.
2. **OOB fragment written straight to `w` instead of buffered first.** The
   journal OOB pattern it was copying buffers into `bytes.Buffer` before
   writing; this diff originally didn't. A partial template-execution
   failure would have let a truncated `hx-swap-oob="true"` fragment reach
   the client, which htmx would swap into the DOM — destroying the tree,
   worse than skipping the refresh. **Fixed**: now renders into a
   `bytes.Buffer` and only writes on success, matching `pos_api.go`'s
   pattern exactly.
3. **Go test didn't anchor the highlight to `catalog` specifically** — it
   checked "exactly one `is-current` link exists" without checking *which*
   link carries it, so a mis-wired `currentID` could still pass all four
   original assertions. **Fixed**: the test now finds the `catalog` anchor
   tag specifically and asserts `is-current`/`aria-current` land on that
   tag's own markup, not just somewhere in the fragment.
4. **E2E test's explanatory comment was factually wrong.** It claimed "the
   first click already worked" — false for the test's actual flow (starts
   at the bare `/help` index, so the very first click already exercises the
   bug; the independent review proved this by reverting and watching the
   test fail at the first click's assertion, not the second). **Fixed**:
   rewrote the comment to describe the real mechanism (every htmx swap
   shows whichever highlight was correct at the last full page load, so
   even a first click after loading the bare index already fails) and
   renamed the test accordingly. The two-click structure stays — it also
   rules out a fix that only special-cases one hardcoded topic, and it
   covers browser back/forward.
5. **htmx history-cache-miss edge case (pre-existing, deferred).** htmx's
   `historyCacheSize` is 10; the manual has 21 topics. On a cache miss htmx
   re-requests with `HX-Request: true` *and*
   `HX-History-Restore-Request: true`, and `renderHelpPage` only checks the
   former, so a restore could get a fragment instead of a full page. This
   predates this diff (the fragment-only response already existed) and this
   change makes it no worse — the nav now at least rides along when it
   happens to swap correctly. Filed as a new Backlog card
   (universaltill/ut-docs#433) rather than folded into this fix, since it's
   a distinct bug with its own scope.
6. **`/help/search` leaves a stale highlight showing while results are on
   screen (pre-existing, accepted).** Confirmed this is the right call, not
   an oversight: a search-*result* click routes through the same
   `GET /help/{topic}` handler as a tree click (`help_results.html` uses
   the identical `hx-get="/help/{{ .Topic.ID }}"`), so it already gets the
   OOB fix for free. Only the transient "results are showing, old topic
   still lit" cosmetic state is untouched, and that's a product call
   (should the tree clear entirely while searching?), not an engineering
   gap in this card's scope.

### Checked and clean (verified, not assumed)

- Repository-pattern / money / i18n / offline-first: no SQL, no money, zero
  `T` calls added or removed (pure structural move + an `OOB` flag), no file
  I/O anywhere in the diff (so the two recurring bug classes — missing
  `os.MkdirAll`, a cwd-relative path where `paths.Data(...)` belongs — don't
  apply; confirmed by reading, not assumed).
- RTL/logical CSS: `class="manual-tree"` unchanged, so existing RTL rules
  keep applying; no `left`/`right` introduced.
- No real client/shop name, no secret-shaped literal.
- **Manual (`web/help/`):** grepped every `web/help/en/*.md` topic for
  nav/tree/highlight prose — nothing describes this chrome-level behavior,
  so nothing has gone false and no topic update is required.
  `guard-help-topics.sh` green (no new routes).
- `README.md`: nothing it claims is affected by this diff.

## Verified beyond automated tests

- **TDD claims re-verified independently, twice** — once by Dev/Tester
  (this pipeline's own revert-and-restore before handoff), once again by
  the independent reviewer from scratch: reverted
  `internal/httpx/httpx.go`, `internal/pages/help_page.go`,
  `web/ui/pages/help.html` and moved `internal/ui/help_nav.go` /
  `web/ui/partials/help_nav.html` aside, re-ran both the Go test and the
  Playwright test, confirmed both fail on the exact claimed symptom
  (fragment missing the OOB tree; `a[href="/help/catalog"]` never gains
  `is-current`), restored, confirmed both pass.
- Locale propagation probed live for en/fa/es — the OOB fragment's tree
  content is genuinely translated (Persian topic titles), not just the
  panel.
- OOB-id uniqueness probed on real rendered output: exactly one
  `id="manual-tree"` in a full-page response, exactly one (with
  `hx-swap-oob="true"`) in an htmx response — never a duplicate.

## Gate — green

`go build ./...`; `go vet ./...`; `go test ./internal/pages/... ./internal/ui/... ./internal/httpx/... -run Help -v` (19/19 pass); full `go test ./... -race`
(one pre-existing failure, `internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure`
— root-sandbox artifact, ut-docs#415, this diff touches no Go in that
package); `guard-data-access.sh`; `guard-i18n.sh` (856 keys, all locales
match); `guard-help-topics.sh`; `gofmt -l` clean on every file this diff
touches (4 unrelated pre-existing files flagged elsewhere, untouched here).

Playwright `manual.spec.ts` (both review passes): 15/15 passed. Full
`--project=default` suite (pre-fix-round pass): 105/106 — the one failure is
`catalog-image-to-till.spec.ts`, a PNG-decode timing issue specific to this
sandbox's substituted Chromium build, unrelated to this diff and already
documented in ut-docs#423's own review record.

## Verdict

**Safe to merge.** Minimal, correct, follows established codebase
convention exactly, both new tests independently proven non-vacuous, no
manual/README update owed, one small deferred follow-up card filed
(universaltill/ut-docs#433, htmx history-cache-miss on `/help/{topic}`).
