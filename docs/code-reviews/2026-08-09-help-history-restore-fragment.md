# Code review: /help/{topic} returns a bare fragment on an htmx history-cache-miss restore

**Card:** universaltill/ut-docs#433
**Date:** 2026-08-09
**Complexity:** easy — Dev inline (Sonnet), Review via an independent
fresh-context Sonnet subagent (isolated worktree). One review round: no
blockers found, so no second round per this pipeline's process-depth rule.

## What shipped

htmx caps `historyCacheSize` at 10 snapshots
(`web/public/vendor/htmx.min.js` default), but the manual has well over
10 topics. When the browser needs to restore a `/help/{topic}` URL whose
snapshot has aged out of htmx's cache, htmx re-requests the URL itself
with **both** `HX-Request: true` and `HX-History-Restore-Request: true`
set — and expects a full-page response back so it can replace the whole
tracked history element, not the bare reading-panel fragment an ordinary
in-page htmx topic-swap gets.

`internal/pages/help_page.go`'s `renderHelpPage` only checked `HX-Request`,
so a history-restore request wrongly got the fragment-only response
(missing the page shell, nav tree, etc.).

The fix adds a check for `HX-History-Restore-Request` and falls through
to the full-page render (`httpx.Render("ui/pages/help.html", …)`) when
that header is set, even though `HX-Request` is also true:

```go
isHistoryRestore := strings.EqualFold(r.Header.Get("HX-History-Restore-Request"), "true")
if strings.EqualFold(r.Header.Get("HX-Request"), "true") && !isHistoryRestore {
    httpx.RenderPartial("ui/partials/help_topic.html", data)(w, r)
    ...
    return
}
httpx.Render("ui/pages/help.html", data)(w, r)
```

An ordinary in-page htmx topic swap (no history-restore header) is
unaffected — still gets the fragment + OOB nav swap it always did.

## Test added (TDD)

`TestHelpTopicHistoryRestoreReturnsFullPage` in `internal/pages/help_page_test.go`:
a synthetic request with both `HX-Request: true` and
`HX-History-Restore-Request: true` set, asserting the response contains
the full-page shell marker (`class="manual-nav"`) as well as the topic
content (`data-topic="catalog"`). This matches the issue's own acceptance
criteria ("a synthetic request with both headers set is enough to pin the
handler behavior") rather than a slow/flaky >10-navigation browser replay.

Confirmed test-first: ran against the pre-fix code, failed with
`history-restore response did not render the full page shell`; then
implemented the fix and confirmed it passes.

## Independent review

A fresh-context Sonnet subagent, isolated in its own git worktree,
independently:

- Re-derived why the bug is real (checked `help_nav.html`/`help_results.html`
  both set `hx-push-url="true"`, confirmed the manual has well over 10
  topics, so htmx's cache genuinely gets exceeded in practice).
- Checked the OOB-nav-swap code path directly below the fix stays inside
  the (unchanged) fragment branch — no risk of a full-page response
  accidentally carrying duplicated OOB nav markup.
- Checked the only other `HX-Request` check in `internal/pages`
  (`auth_page.go`'s logout → `HX-Redirect`) — unrelated, not a
  history-tracked page response, no parallel bug.
- **Independently re-verified the TDD claim**: reverted just the fix in
  its own worktree, re-ran the new test, confirmed the same failure;
  restored the fix (byte-identical, confirmed via `git diff`), confirmed
  the test passes again.
- Ran the full gate itself: `go build ./...`, `go vet ./...`,
  `go test ./internal/pages/... -race` (all green), `gofmt -l` (clean),
  `guard-i18n.sh`, `guard-data-access.sh`, `guard-help-topics.sh` (all
  passing).
- Confirmed scope: diff is exactly the two files above, no SQL/money/
  i18n-string/plugin/kiosk-engine code touched.
- Confirmed no manual (`web/help/`) update is owed — this restores
  intended browser back/forward behavior for the existing `/help` page;
  no new page, no new UI, no changed shop-owner-facing steps, and no
  existing topic describes (or should describe) htmx-internal behavior.

**Verdict: SAFE TO MERGE.** One non-blocking nitpick noted (the new test
could additionally assert the OOB-tree markup is absent from the
full-page response, for symmetry with the sibling fragment test) — not
fixed, since the code path structurally cannot produce both branches at
once (verified by review), so the extra assertion would not catch any
reachable regression.

## Post-review fix: guard-docs-shots.sh

`guard-docs-shots.sh` was not run in the local gate before review (an
omission in this cycle's own checklist, not caught by the reviewer
subagent either — its guard list didn't include it). CI caught it: the
manifest's `surface_sha256` hashes every non-test `*.go` under
`internal/pages/`, so editing `help_page.go` — even with zero visible
change — requires a `make docs-shots` regen and manifest rewrite, or the
guard fails.

Ran the real Playwright capture (`npx playwright test
--config=playwright.docs.config.ts`, all 60 shots, then
`node tests-docs/write-manifest.js`) rather than hand-writing the
manifest — `write-manifest.js`'s own comment is explicit that it must
only run after a real capture, so the manifest never describes
screenshots that were not actually taken. (Environment note, not a repo
change: this sandbox's pre-installed Chromium revision didn't match the
pinned `@playwright/test` version's expected headless-shell revision, so
the capture ran with a temporary, uncommitted `launchOptions.executablePath`
override pointed at the pre-installed full Chromium binary, reverted
before committing — real CI installs its own matching browser via the
workflow's own `playwright install` step and needs no such override.)

Diffed the regenerated PNGs against their prior versions: only
`alerts.png` (all 4 locales) and `designer.png` (all 4 locales) plus
`fa/sell.png` changed pixels, and every diff is a startup-time log
timestamp in the "Recent problems" panel (`07:38` → `08:49`-class),
already-known non-deterministic content unrelated to this fix — see the
open backlog card for `designer`'s own pinned-time follow-up
(ut-docs#360). No content this change could plausibly affect (topic
markdown hashes, template/CSS surface) changed; confirmed by the fact
`manifest.json`'s per-topic hashes are unchanged — only `surface_sha256`
moved, exactly as expected from a Go-only source edit.

## Verified beyond automated tests

- Full `go test ./... -race` across the whole module (run by Dev/Tester
  before handoff) — all packages green.
- All four `universal-till` guard scripts green
  (`guard-data-access.sh`, `guard-i18n.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`) plus `guard-help-topics.sh`.
- No visible-surface change (pure response-selection logic for an
  htmx-internal request class), so no screenshot/browser drive was
  performed — the synthetic-header httptest is the correct verification
  shape here, matching the issue's own acceptance criteria.

## Deferred / out of scope

Nothing found requiring a follow-up card.
