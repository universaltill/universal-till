# Code review: page-route errors render through the base layout — ut-docs#1455

**Branch:** `fix/1455-page-error-render` · **Reviewer:** independent Opus
subagent (complexity:medium → Opus review per the `scrum-master` skill's
model-routing table), two rounds (first found blockers, second scoped to
the fixes)

## Incident

Live on the TECLAST tablet (v0.9.1-1423b), reported by the product owner:
opening Tables & floor plan showed "failed to load tables" on an otherwise
white screen, and the till was stuck there — the kiosk hides Android's own
navigation bar, so there was no way back short of an adb `KEYCODE_BACK`.
Root cause: `internal/pages/tables_page.go`'s `tiles()` helper answered a
repo failure with a bare, unlogged `http.Error(w, "failed to load tables",
500)` — on a pinned WebView that replaces the ENTIRE document with plain
text, no rail, no way back.

Investigation found the defect was not scoped to `/tables`: any page-route
handler answering with a bare `http.Error`, or with `common.LocalizedError`/
`common.LogAndLocalizedError` (translated, but still `http.Error` under the
hood — same full-document replacement), produces the identical lock-up.

## Change

- **`internal/httpx/render_error.go` (new)** — `RenderError(w, r, status,
  msgKey, err)` renders a translated error page through the normal
  `base.html` layout (rail + "Back to sale") instead of a bare error body.
  Logs the real error server-side (feeds `logging.Recent()` at 5xx, same
  convention as `LogAndLocalizedError`), sets `Cache-Control: no-store`.
  Builds the template *before* writing status/headers, falling back to
  plain `http.Error` on a template-build failure rather than
  `template.Must` panicking after a partial response (review finding,
  round 1).
- **`web/ui/pages/error_page.html` (new)** — the rendered content block.
- **`tables_page.go`** — the actual incident. `tiles()` now returns the
  error to its two callers instead of writing to `w` itself: the full
  `/tables` page renders through `RenderError`, the `/ui/tables/state`
  HTMX fragment keeps a short `LogAndLocalizedError` body. This also fixes
  the "not logged anywhere" gap found alongside the bare-text bug. A
  second gate, `requirePageManager`, mirrors `requireManager`'s check but
  answers via `RenderError` — used only by the `/tables` GET route, since
  `requireManager` itself is shared with `/ui/tables/state` and several
  `/api/tables/*` POST routes that must keep their short body (review
  finding, round 1: the incident's OWN page still 403'd with a bare body
  one call away from the rest of the fix).
- **Guard (`scripts/ci/checkpagehttperror` + `guard-page-http-error.sh` +
  `_test.sh`, new)** — AST-based, fails CI on a bare `http.Error`/
  `common.LocalizedError`/`common.LogAndLocalizedError` inside a
  page-route handler: an inline closure, a same-package factory-call
  handler, or — round 1 finding — transitively through a **local closure**
  the handler calls (`name := func(...) {...}` declared earlier in the
  same `registerXxx` function, e.g. `tiles`/`requireManager`'s own shape).
  A reviewed exception carries a same-line `// page-error:allow <reason>`.
- **7 real page-route sites migrated** to `httpx.RenderError`: `/tables`
  (2 sites), `/my-reports`, `/users/permissions` (×2),
  `/plugins`, `/plugins/store`.
- **~40 remaining sites marked, not migrated** — the widened guard (round
  1's B1/B2 fixes) surfaced ~40 more page-route sites across ~20 other
  files using the `LocalizedError` wrapper or a local-closure helper.
  Migrating all of them here would balloon this PR's diff well past a
  single reviewable change; each carries a same-line `// page-error:allow
  ... ut-docs#1458`, and the follow-up migration is tracked as
  [ut-docs#1458](https://github.com/universaltill/ut-docs/issues/1458).
  The guard is fully enforcing either way — zero unmarked gaps.
- **Two deliberate, permanent exceptions**: `setup_page.go`'s
  pre-enrollment wizard and `order_tracking.go`'s anonymous customer
  surface — neither uses the base layout (no rail to render into, or no
  sale screen to link back to).
- New i18n keys (`error.page.title`, `tables.error.load_failed`,
  `permissions.error.super_admin_required`) in en/ar/fa/tr.
- Wired into `ci.yml`'s build job and `CLAUDE.md`'s guard list.
- Screenshots regenerated (`make docs-shots`, twice — once for the
  initial migration, once more after the `page-error:allow` comments
  landed, since the guard hashes the whole `internal/pages/**.go` surface
  including comments).

**Out of scope, split into follow-up cards:**
- [ut-docs#1457](https://github.com/universaltill/ut-docs/issues/1457) —
  a native Android `WebViewClient.onReceivedHttpError`/`onReceivedError`
  bar, for a main-frame failure that never reaches our Go handler at all
  (server not up yet, network blip mid-navigation). Materially separate
  client-side work sharing no mechanism with this fix.
- [ut-docs#1458](https://github.com/universaltill/ut-docs/issues/1458) —
  migrate the ~40 `page-error:allow`-marked sites above.

## Review round 1 — findings and fixes

**B1 (blocker, fixed).** The guard's first version matched only the
literal `http.Error` token. `common.LocalizedError`/
`common.LogAndLocalizedError` are `http.Error(w, translatedText, status)`
under the hood — translating the text doesn't add the rail back. 28 real
page-route sites used this wrapper, two of them adjacent to lines the
first version had migrated (`my_reports_page.go:69`, one line above the
fix at `:76`; `tables_page.go`'s own `requireManager`, inside the very
handler the card is named after). Fixed: guard extended to flag both
wrapper calls; `tables_page.go` and `my_reports_page.go` migrated; the
other 26 marked `page-error:allow` → ut-docs#1458.

**B2 (blocker, fixed).** The guard's first version didn't follow a
handler's call into a locally-declared closure — exactly the shape
`tiles()` itself had (the reported incident's own root cause), so the
guard could not have caught the bug it was written for. 16 more real
sites had this shape. Fixed: `localClosures()` + a transitive `scan()`
follow a handler into any `name := func(...)` closure declared in the
same enclosing function, bounded against cycles; the guard's success
message no longer claims more than it checks. New unit tests
(`TestFindViolationsFollowsLocalClosureIndirection`,
`TestFindViolationsLocalClosureSharedWithApiRoute`) cover the exact
tables.go shape and confirm a closure shared with a POST/API route is
still scoped correctly (flagged only for the page route).

**B3 (blocker, fixed).** The e2e spec intercepted `/tables` and fulfilled
a response body the test itself authored — it never touched the real
server, and stayed green with the Go fix fully reverted. Fixed by
deletion, not replacement: no page migrated in this PR has a
deterministic real-failure path reachable under the e2e harness's
`UT_AUTH=off` + default marketplace config (confirmed live: booting an
e2e-style server shows `/plugins/store`'s `CatalogRepo` is non-nil there
by default, so even that 503 branch isn't reachable). Real coverage is
the Go handler-level integration test (drives the actual repo failure
through the real mux) plus a manual, real-server, real-browser check
(below) — a stated, accepted gap rather than a test that would pass
against broken code.

**N1–N3 (non-blockers, fixed):** `RenderError` now builds the template
before writing status/headers (a template failure degrades to plain
`http.Error` instead of panicking mid-response); dropped `error_page.html`'s
`helpLink "error"` call (no matching manual topic — simpler than adding
one for a generic error page); the nil-error log line no longer prints a
stray `<nil>`.

**N4–N11:** reviewed, accepted as-is or noted — the dropped
`logging.L().Errorf` in `permission_settings_page.go` is correctly
superseded by `RenderError`'s own logging; the hardcoded `←` glyph in
`error_page.html` mirrors 12+ existing pages verbatim (pre-existing
repo-wide RTL convention, confirmed correct via Unicode bidi mirroring in
the actual screenshot below, not this PR's to change); the four locale
files' incidental `products.title`/`products.uncategorized` reformat and
the docs-shots PNG churn are harmless.

## Review round 2 — scoped to the fixes above

Re-verified independently, not just re-running the guard and trusting
green: copied `internal/pages` to a scratch dir, stripped every
`page-error:allow` comment, and re-ran the checker — **45 sites detected**,
an exact match against round 1's own independent AST analysis (all 16
helper-indirected sites, all 28 `LocalizedError`/`LogAndLocalizedError`
sites, plus the 3 permanent exceptions). `tables_page.go:66` and
`my_reports_page.go:69` — the two blockers named in round 1 — are
confirmed gone from that list. 42 of the 45 markers cite ut-docs#1458; the
remaining 3 are exactly the permanent architectural exceptions
(`setup_page.go`, `order_tracking.go` ×2) and ut-docs#1458's own exit
criterion ("zero markers **referencing this issue**") is worded so those
three don't dilute it.

- **`requirePageManager` split** — confirmed sound: a byte-for-byte copy
  of `requireManager`'s check with `RenderError` swapped in;
  `requireManager` itself is untouched and still serves the fragment/API
  routes, so no behavior change there.
- **`TestTablesPagePermissions`** — confirmed real by running it in
  isolation (`-run 'TestTablesPagePermissions$' -count=1`): fails against
  the old bare-body 403, passes against the fix.
- **`localClosures`'s flat per-`FuncDecl` scan** — probed the whole tree
  for two failure modes: (1) two same-named local closures colliding
  within one `FuncDecl` (none found — the one adjacent case, `writeJSON`
  as both a package-level func and a local closure in `ai_api.go`, is
  inert since `locals` is scoped per-`FuncDecl` and package-level funcs
  are never followed), and (2) the `var f func(...); f = func(){...}`
  recursive-closure idiom, which `localClosures` can't see (requires
  `:=` or `var x = func`) — zero live instances of that shape either.
  The `visited`-by-name cycle guard is sound: `locals` rebuilds per
  `FuncDecl` and `visited` resets per route, so same-named closures in
  different functions can't cross-contaminate.
- **B3 (dropped e2e spec)** — agreed dropping was correct. One
  real-failure path does exist today (the `auth` Playwright project runs
  with real auth, so a cashier hitting `GET /tables` there would produce
  the genuine 403 through no interception) but reaching it needs a new
  admin→create-cashier→re-login flow for a spec that would only cover the
  403 path (already covered by `TestTablesPagePermissions` at the Go
  layer and by the existing `nav-rail-lock-reachable-1346.spec.ts` for
  "the rail paints and is clickable") — not the reported 500 repo
  failure, which has no deterministic trigger under either e2e project.
  Noted as an option on ut-docs#1458, not worth blocking on.
- **N1/N2/N3** — confirmed resolved; N3 specifically verified in real
  `-v` output (`[page-error] GET /tables 403`, no trailing `<nil>`), not
  just by the non-panic test.

Minor notes for the next reader (not fixed, not blocking): `page-error:allow`
now carries two meanings (3 permanent exceptions vs. 42 tracked TODOs) —
worth a line in the guard's doc comment; `bareErrorCall` matches the
literal identifier `common` and would miss an aliased import (no file
does this today).

## Tests

- `internal/httpx/render_error_test.go`: full-layout rendering
  (rail/Back-to-sale/translated message present, raw error absent,
  `Cache-Control: no-store`), per-locale translation, nil-error path
  doesn't panic.
- `internal/pages/tables_page_test.go`:
  `TestTablesPage_RepoFailureRendersErrorPageNotBareText` (real `DROP
  TABLE tables`, asserts rail + Back + translated text in the response,
  the raw driver error absent from the response but present in
  `logging.Recent()`), `TestTablesLiveState_RepoFailureStaysAFragment`
  (the HTMX partial stays a short fragment, not a full document),
  `TestTablesPagePermissions` extended to assert the rail is present on
  the cashier's 403.
- `scripts/ci/checkpagehttperror/main_test.go`: inline closure, factory-
  call handler, `LocalizedError`/`LogAndLocalizedError`, local-closure
  indirection (plain and shared-with-an-API-route), allow-marker,
  non-page-route exclusion, and a standing regression test that the real
  tree stays clean.
- `scripts/ci/guard-page-http-error_test.sh`: the same fixture matrix as
  a bash-level CI regression test.
- TDD-verified: mentally (and, for `tables_page.go`, actually) reverting
  each fix makes its matching test fail with the expected symptom
  (missing rail class, untranslated body, guard reporting 0 violations
  where there should be several).

## Verification beyond automated tests

- **Real server, real failure, real browser.** Built the actual binary,
  ran it against a fresh sqlite data dir, dropped the `tables` table on
  the live database file, hit `GET /tables` with curl (real 500,
  confirmed nav rail + nav-toggle markup present), then screenshotted
  with Playwright against the running server in both `en` and `fa`
  (RTL): rail visible and correctly mirrored to the right, message and
  "Back to sale" button rendered, RTL arrow glyph correctly bidi-mirrored
  by the browser (not a CSS/markup fix of this PR's — pre-existing
  Unicode behavior, confirmed working). Server killed and its temp data
  dir removed after.
- e2e: ran the full `tables-*` spec suite (9 specs) against the real e2e
  harness after every change in this PR — green throughout, including
  after the `requirePageManager` split.
- Gate: `gofmt -l .` clean; `go build ./...` / `go vet ./...` clean;
  `go test ./...` full suite green (43+ packages); `guard-page-http-error.sh`
  (+ its `_test.sh`), `guard-i18n.sh`, `guard-help-topics.sh`,
  `guard-docs-shots.sh`, `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-compliance-claims.sh`, `guard-e2e-fixtures-import.sh` all pass.

## Verdict

**Safe to merge.** Both review rounds ran real verification (build/vet/
test/guards, plus an independent re-derivation of the guard's detection
surface with the allow-markers stripped, not just trusting a green run)
rather than reading the diff alone. Deferred, tracked: ut-docs#1457
(native Android error bar) and ut-docs#1458 (~40 remaining page-route
sites, each already marked and guard-enforced against silent regrowth).
