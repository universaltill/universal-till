# Code review: remove the redundant in-content Menu button from /admin (ut-docs#2166)

**Date:** 2026-09-12
**Card:** universaltill/ut-docs#2166
**Author:** pipeline (`lane:cloud-24`), Sonnet
**Reviewer:** independent fresh-context Sonnet subagent (isolated worktree)

## What shipped

The product owner reported, live on the pilot tablet (v0.14.13), that the
in-content "Menu" button in `/admin`'s page head — added deliberately by
ut-docs#2116 — is redundant with the nav rail's own Menu icon, which is
present on every page and already reaches `/menu`. This card reverses that
one AC of #2116 at the product owner's explicit request.

- `web/ui/pages/admin.html`: removed the in-content
  `<a class="btn primary btn-touch" href="{{ .BackHref }}">…Menu</a>`
  button and its explanatory comment from the page head. The head now
  renders just the `<h1>` + help link, matching the single-child
  `.page-head` pattern already used elsewhere (e.g. `tables.html`).
- `internal/pages/admin_page.go`: removed the now-unused
  `"BackHref": "/menu"` entry from the template data map. Confirmed via
  repo-wide grep that `BackHref` has zero remaining references anywhere.
- `internal/pages/admin_page_test.go`:
  `TestAdminPage_AllNonEmptyClustersRenderForFullAccessDETill`'s
  "can still get back" assertion changed from
  `strings.Contains(body, `href="/menu"`)` to
  `strings.Contains(body, `data-testid="nav-menu"`)`. The old assertion was
  never actually testing the in-content button specifically — the nav rail
  *also* renders `href="/menu"` unconditionally on every page, so the old
  check would have kept passing even with the button already gone. The new
  assertion targets the nav rail's own stable e2e test id
  (`internal/httpx/rail.go`'s `railPresentation["/menu"]`), which only the
  rail renders.
- `web/help/img/manifest.json`: one-line `surface_sha256` refresh via the
  documented escape hatch (`scripts/ci/update-docs-shots-surface-hash.sh`),
  not a full `make docs-shots` regeneration — see "Verified beyond
  automated tests" below for why that's correct here.
- Sub-page sweep: the card also asked to check the other admin
  destinations (locations, registers, fiscal_register, fiscal_device,
  country_settings, translations) for the same duplicate-of-the-rail
  pattern. Verified none of them carry it — only `admin.html` did — so no
  other file needed a change.

## Independent review

Fresh-context Sonnet subagent, isolated worktree (never saw the
implementation reasoning). **Verdict: SAFE TO MERGE, no findings** (no
blocker, should-fix, or nit).

Ran for real, not just read: `gofmt -l`, `go build ./...`, `go vet ./...`,
`go test ./internal/pages/... -run TestAdminPage -v` (all 18 subtests
green), `golangci-lint run ./...` (0 issues), `guard-i18n.sh`,
`guard-help-topics.sh`, `guard-docs-shots.sh`,
`guard-docs-shots-cross-check_test.sh` (all green).

**TDD-vacuousness check on the new assertion** (fault injection, not just
reading the diff): temporarily renamed the rail's `nav-menu` testid to
`nav-menu-BROKEN` in `internal/httpx/rail.go` — confirmed
`TestAdminPage_AllNonEmptyClustersRenderForFullAccessDETill` genuinely
**fails** in that state, then restored the file and confirmed it **passes**
again. This proves the replacement assertion is testing something real,
not trivially always-true the way the assertion it replaced was.

**docs-shots escape-hatch claim, independently re-derived**: parsed every
`routes:` front-matter line under `web/help/en/*.md` — only `menu.md`
mentions `/admin` at all, and there it's the *third* entry
(`routes: [/menu, /settings/menu, /admin]`), never `routes[0]` (the one URL
`docs-shots.spec.ts` actually visits per topic). No topic anywhere has
`/admin` as its first route, so `/admin` is never actually captured by any
of the 104 screenshots — confirming the hash-only update was the correct
mechanism, not a shortcut around a real pixel change.

Also checked and clear: no missing `os.MkdirAll`/cwd-relative-path bug
class (diff touches no file I/O), no real client/shop name or
secret-shaped literal anywhere in the diff, no stale "back to menu button"
prose left in any `web/help/en/*.md` topic, nav rail's `/menu` entry is
unconditionally rendered (`uislot.CoreRail` has no `VisibleIf`/gating on
it) so no user can be stranded on `/admin` without a way back.

## Verified beyond automated tests

Drove the real app for this session's own Tester pass (not just Go tests):
started the server (`UT_AUTH=off`), navigated to `/admin` at the 1024×600
kiosk floor with Playwright/Chromium, confirmed visually (screenshot
inspected) that the page head shows only "Administration" + the help icon
with no leftover button or whitespace artifact, confirmed
`[data-testid="nav-menu"]` is present exactly once and its `href` is
`/menu`, and confirmed clicking it actually navigates to `/menu`.

## Safe-to-merge verdict

Yes. Minimal diff, exactly matches the card's stated scope, zero dangling
references to the removed field, the test-assertion change is a genuine
strengthening verified by fault injection (not just churn), and the
docs-shots hash-only update is legitimate per the guard's own algorithm
and independently re-derived by the reviewer.

## Explicitly deferred

Nothing deferred — the card's full scope (remove the button, sweep sibling
admin pages, keep the "can still get back" test meaningful) is complete in
this change.
