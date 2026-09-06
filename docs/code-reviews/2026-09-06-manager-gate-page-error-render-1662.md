# Code review: migrate 12 manager-gate 403 page routes to httpx.RenderError

**Card:** universaltill/ut-docs#1662 (split from #1458, itself split from #1455)
**PR:** universaltill/universal-till#TBD (branch `fix/1662-manager-gate-page-error-render`)
**Complexity:** easy — Dev at Sonnet (inline), Review at a fresh-context Sonnet subagent.

## What shipped

#1455 added `httpx.RenderError` (renders a translated error page through
the full base layout — rail + "Back to sale" — instead of a bare,
full-document error body) and a CI guard flagging any page-route handler
still using a raw `http.Error`/`common.LocalizedError`/
`common.LogAndLocalizedError`. It migrated 7 real sites and marked the
remaining ~40 with `// page-error:allow ... ut-docs#1458` rather than
ballooning that PR.

This change migrates the 12 of those ~40 sites that share one homogeneous
shape — a manager/admin 403 check reusing the existing
`common.error.manager_or_admin_required` i18n key:

- `promotions_page.go`, `country_settings_page.go`, `tax_codes_page.go`,
  `backoffice_page.go`, `issue_report_page.go`, `users_page.go`,
  `kitchen_stations_page.go`, `fiscal_register_page.go`,
  `registers_page.go`, `locations_page.go`, `audit_page.go`: direct swap
  of the `common.LocalizedError` call for `httpx.RenderError(w, r,
  http.StatusForbidden, "common.error.manager_or_admin_required", nil)`.
  For 6 of these (promotions, country_settings, kitchen_stations,
  fiscal_register, registers, locations) the check lives inside a
  `requireManager` closure shared with other (`POST /api/...`) routes in
  the same file — verified every one of those routes is a plain
  `<form method="post">` full-page navigation, not an htmx fragment, so
  migrating the whole shared closure is correct (not just the page's own
  GET, which would have needed `tables_page.go`'s two-gate split).
- `translations_page.go`: its `requireManager` genuinely IS shared with
  htmx fragment routes (`GET /ui/translations-table`, `POST
  /api/translations/set`, `POST /api/translations/clear` — all
  `hx-get`/`hx-post` with `hx-target`), so this one needed the
  `tables_page.go`-style split: a new `requirePageManager` (RenderError)
  for `GET /translations` only, `requireManager` (LocalizedError)
  unchanged for the fragment routes.

No i18n changes — the key already exists in all 4 locales (introduced by
#1455). No new UI surface — reuses the same, already-shipped error
template `#1455` verified visually; no manual/screenshot update needed.

## Independent review

A fresh-context Sonnet subagent (this card's `complexity:easy` review
tier), in an isolated worktree (`.claude/worktrees/review-1662`, detached
at the review commit), reviewed the diff independently. **Verdict: safe to
merge, no blocking issues.**

What it verified, concretely (not just re-reading the diff):
- **The core risk** — that migrating a *shared* closure for the 6
  plain-form files wouldn't silently break an htmx-fragment caller —
  by grepping every template under `web/ui/pages/*.html` for
  `hx-get`/`hx-post`/`hx-put`/`hx-delete` against every route each
  closure gates. Zero htmx callers found among those six; every non-GET
  route confirmed a plain full-page form post.
- The `translations_page.go` split specifically, against the real
  templates (`translations.html`, `translations_table.html`) — confirmed
  the fragment routes really are `hx-get`/`hx-post` with
  `hx-target="#translations-table"`, and the new `requirePageManager` is
  wired only to `GET /translations`.
- Full gate: `go build ./...`, `go vet ./...`, `gofmt -l internal/pages/`,
  `go test ./internal/pages/...`, `go test ./...` (full module, 49 `ok`
  packages, 0 failures), `golangci-lint run ./internal/pages/...` (0
  issues), `bash scripts/ci/guard-page-http-error.sh` (pass — marker count
  44 → 31, matching the commit's claim), `bash scripts/ci/guard-i18n.sh`
  (pass).
- **TDD re-verification, personally, on 4 of the 12 sites**
  (`translations_page.go`, `promotions_page.go`, `kitchen_stations_page.go`,
  `tax_codes_page.go`): reverted each file's production change back to the
  pre-fix version, re-ran that file's own test, confirmed the exact
  expected failure (`"... has no nav rail:\nManager or admin required"`),
  restored the fix, confirmed the test passed again.
- i18n key coverage: `common.error.manager_or_admin_required` resolves to
  a real, non-empty string in all 4 locales (en/ar/fa/tr) — no gap.
- No file-write/`os.MkdirAll` or cwd-relative-path issues (not applicable
  — pure response-rendering change). No real client/shop names, no
  secret-shaped literals.

**One non-blocking finding, fixed before merge:** a cosmetic inaccuracy in
`translations_page.go`'s new `requirePageManager` doc comment, which
claimed the fragment routes use `hx-swap="outerHTML"/"innerHTml"` when all
four `hx-swap` occurrences in the real templates are `outerHTML` only.
Corrected in this same branch (no behavior change).

**One real-but-genuinely-inert edge case, noted and left as-is:**
`kitchen_stations.html`'s "discover printers" button calls
`POST /api/kitchen-stations/discover-printers` via a plain JS `fetch()`
(not htmx, not a form) whose `.then()` chain checks `resp.ok` and throws
*before* ever calling `.json()` on a non-2xx response — so it never
parses the error body at all. Switching that body from bare text to full
HTML on a 403 is behaviorally inert for this specific caller; confirmed
via the existing `TestDiscoverPrintersAPI_RejectsNonManager`, which only
asserts on status code and passes unchanged.

## Verified beyond automated tests

- Manually re-derived, for each of the 6 "shared closure" files, that
  every route the closure gates renders a real `<form method="post">` in
  its template with no `hx-*` attribute — a static, verifiable claim, not
  a runtime assumption.
- Confirmed the 31 remaining `page-error:allow ... ut-docs#1458` markers
  (tracked separately in #1663) are untouched and the guard still passes
  with them present.

## Deferred / explicitly out of scope

- The remaining ~31 `// page-error:allow ut-docs#1458` sites (bespoke
  500-error messages, most needing a new or reused per-file i18n key) —
  tracked in ut-docs#1663.
