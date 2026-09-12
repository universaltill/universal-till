# Review: a locked session redirects to /login instead of "Etwas ist schiefgelaufen" (ut-docs#2144)

## What shipped

On the pilot tablet, a session that expired mid-sale (idle lock) left the
cashier looking at a fully rendered sale screen wearing the generic
"Something went wrong. Please try again." banner — advice that cannot work,
because retrying without signing in fails identically forever.

Root cause: `internal/auth/middleware.go`'s `Middleware()` answered a bare
JSON 401 for **every** `/api/*` request with no valid session, including one
carrying `HX-Request: true`. htmx discards a JSON body on a non-2xx response
and fires `htmx:responseError` instead, which `web/public/app.js`'s global
handler renders as the generic `#pos-alert` banner. `hold`/`resume`/`scan`
are all `hx-post` forms under `/api/pos/*`, so all three hit that branch;
`tender` is a raw `fetch()` that sends no `HX-Request` at all and had its
own separate bug (rendering the raw JSON error envelope as the payment
status line).

- `internal/auth/middleware.go`: the `/api/*` JSON-401 branch now also
  requires `r.Header.Get("HX-Request") != "true"`, so an htmx caller under
  `/api/*` falls through to the same `HX-Redirect: /login` logic every other
  route already gets. Only a **non**-htmx `/api/*` caller (mobile/sync
  clients) keeps the JSON contract. Doc comment restated to match.
- `internal/auth/auth_test.go`: new
  `TestMiddlewareHXRequestUnderAPIPathGetsRedirectNotJSON` drives the real
  `Middleware()` over `/api/pos/{hold,resume,scan,tender}` for both an htmx
  caller (401 + `HX-Redirect: /login`, no JSON body) and a non-htmx caller
  (the unchanged 401 JSON envelope, and no `HX-Redirect` it never asked for).
- `web/public/app.js`: the tender panel's raw `fetch('/api/pos/tender')` now
  checks `response.status === 401` before the generic-text branch and
  navigates to `/login` instead of painting the JSON envelope into the
  status line.
- `e2e/tests/session-expiry-redirect-2144.spec.ts` (new) +
  `e2e/playwright.config.ts` (`AUTH_ONLY_SPECS`): a real Playwright/Chromium
  spec on the `auth` project — real PIN login, real session, real
  server-side revocation via `POST /api/auth/logout` (→ `svc.Logout` →
  `RevokeSession`) — driving `hold` and `scan` through real DOM clicks and
  proving `resume`/`tender` with a real HTTP request carrying the same
  `HX-Request` header htmx sends.

No locale files touched: landing on the existing `/login` PIN screen
satisfies the acceptance criteria, so there is no new user-facing string.

## Independent review

One Opus pass, fresh context, isolated worktree, different model from the
session that implemented this. Six findings; four fixed, one accepted, one
referred to a follow-up card.

### F1 (blocking) — `guard-e2e-fixtures-import.sh` fails. **Fixed.**

The new spec imports `test` from `@playwright/test` rather than
`./fixtures`. That guard is a step in `ci.yml`'s `build` job, so the branch
as delivered would have gone red on its first push:

```
❌ e2e-fixtures-import guard: e2e/tests/session-expiry-redirect-2144.spec.ts
   imports `test` directly from '@playwright/test' instead of './fixtures'
```

The right fix is **not** to switch the import. The guard documents an
objective exemption criterion, already applied to
`nav-rail-lock-reachable-1346.spec.ts` and
`nav-rail-svg-icons-lock-1423.spec.ts`: an auth-project-only spec must be
exempt, because `fixtures.ts`'s `resetPosOncePerFile` posts `/api/pos/reset`
through a bare, cookie-less `request` context, which the `auth` project
answers 401 — the very response this card is about — so the fixture would
silently no-op rather than reset anything. This spec is auth-project-only by
construction (it needs a real session to revoke). Added
`session-expiry-redirect-2144.spec.ts` to `EXEMPT_FILES` with the reasoning
recorded in the guard's header block, in the same style as the two existing
entries. The exemption narrows nothing for any default-project spec, and the
spec cannot leak state either way: every request it makes after revocation
is rejected by the middleware before any handler runs, and it sorts last
among the auth project's specs. Guard and its self-test both pass.

### F2 (blocking) — `guard-docs-shots.sh` fails. **Fixed.**

`web/public/app.js` is part of the manual-screenshot surface fileset
(`web/ui/**` + `web/public/**` + non-test `internal/pages/**.go`), so the
tender fix invalidated the `surface_sha256` recorded in
`web/help/img/manifest.json`:

```
guard-docs-shots: the app surface (web/ui/**, web/public/**, or internal/pages/**.go)
changed since the manual's screenshots were last taken
guard-docs-shots: run `make docs-shots` and commit the result
```

Causation confirmed rather than assumed: substituting `main`'s `app.js` into
the otherwise-unchanged branch tree makes the guard pass (`surface
5323f2a147a9…`), and the branch's own `app.js` makes it fail. Ran
`make docs-shots` — 124 screenshots against real Chromium, all passing —
which updated `manifest.json` (`surface 0b67ca747e6e…`) plus five PNGs
(`{en,fa,ar,tr}/catalog.png`, `ar/till-designer.png`). Guard now passes.
Note this is a hash-freshness requirement, not a pixel one — see "Manual"
below for why no `web/help/` **prose** needed touching.

### F3 (medium) — a real false-pass path in the e2e spec. **Fixed.**

`revokeSessionThenAssertRedirect` swallows every error thrown by
`interact()` (deliberately, for the documented background-poll race). Block 1
guards against that by asserting `#basket` is visible before revoking, but
blocks 2 and 3 did not. So a silently failed re-login would leave the page
sitting on `/login`, make the block-2 locator throw straight into that
`catch`, and let `waitForURL('/login')` pass instantly having exercised
nothing at all. Block 3 had the analogous hole from the other direction:
"no session ever existed" and "session revoked" take the same middleware
branch, so without first proving a session existed, the revocation was not
load-bearing and the two assertions would hold against a till nobody ever
signed in to. Added `await expect(page.locator('#basket')).toBeVisible()`
as the precondition before each, with the reasoning in-line.

### F4 (low) — locale-dependent selector. **Fixed.**

The spec addressed the Hold Sale button as
`page.locator('.tender-default-footer button', { hasText: 'Hold Sale' })`.
`web/ui/pages/index.html:293-299` documents the opposite convention on that
exact element: ut-docs#1629 added `data-testid="tender-footer-hold"`
specifically "so the ... e2e spec can address this directly rather than
matching on visible text (locale-dependent)". The spec's till is English
only because `ensureOperator`'s wizard walkthrough happens to pick GB —
nothing pins it. Switched to the testid (verified unique in `web/ui/`).

### F5 (low) — case-sensitive header compare. **Accepted, no change.**

The new `r.Header.Get("HX-Request") != "true"` at `middleware.go:319` is
case-sensitive, while the codebase's canonical htmx detection
(`httpx.IsFragmentSwap`, `internal/httpx/httpx.go:1137`) uses
`strings.EqualFold`. This is not a defect and not a regression: the new
condition is the exact complement of the pre-existing `== "true"` at
`middleware.go:344`, so the two stay consistent and no request can fall
between them — an `HX-Request: TRUE` caller gets the JSON 401 exactly as it
did before this change. htmx itself always sends lowercase `true`. Changing
one of the two lines and not the other would be worse than leaving both;
a repo-wide `EqualFold` sweep is its own card, not this one's.

### F6 (informational) — same bug class survives on admin raw-`fetch` callers. **Out of scope; follow-up recommended.**

The middleware cannot redirect a caller that isn't htmx, so every other raw
`fetch()` against `/api/*` still surfaces the JSON envelope on an expired
session. The worst shape is `web/ui/pages/plugins.html:121`:

```js
try { var j = JSON.parse(t); if (j && j.error) { m = T[j.error] || j.error; } } catch (_) {}
```

`j.error` is the object `{code, message}`, so that renders `[object
Object]`. `settings.html`, `catalog.html`, `tills.html`,
`bluetooth_devices.html` and `promotions.html` have the same shape. This
card correctly fixed the one instance on the **sale screen** (tender); the
admin-page instances are a separate, lower-severity surface and expanding
scope here was judged wrong. Recommend a follow-up card.

Worth recording that the two htmx-driven `JSON.parse` sites are *improved*
by this change rather than broken by it: `backup_restore_staged.html:75`
(`/api/backup/restart-now`) and `pairing_wait.html:102`
(`/api/sync/pairing-restart`) both did `if (body && body.error) { text =
body.error; }` on an object, so an expired session previously rendered
`✗ [object Object]`; now the page redirects to `/login`.

## Verified beyond the automated gate

**No htmx caller under `/api/*` depends on a JSON error body — checked, not
assumed.** Audited every `htmx:responseError` / `htmx:beforeSwap` /
`htmx:afterRequest` handler in `web/public/*.js` and every `hx-on::`
handler in `web/ui/**` that inspects `event.detail.xhr.responseText`. The
only two that parse it as JSON are the two named in F6, and both improve.
Every other `.json()` / `JSON.parse` in `web/` sits on a raw `fetch()` that
never sends `HX-Request`, so its 401 contract is byte-identical to before.

**Why `#pos-alert` stays hidden.** Confirmed against the vendored
`web/public/vendor/htmx.min.js` that the `HX-Redirect:` header is handled
**before** `isError` / `beforeSwap` are computed and returns early — so
`htmx:responseError` never fires and app.js's `showAlert('server')` never
runs. Same early return means `record-dialog.js`'s `dialogFailureFallback`
no longer stamps "something went wrong" into an open dialog on an expired
session either.

**Security.** `exempt()` (middleware.go:294) and `optionalAuth()` (:307)
both return before the changed branch, so neither tier is affected. Both
401 paths carry the same status and disclose the same fact; the redirect
path returns an **empty** body and a constant `HX-Redirect: /login` — the
`dest` override at :335 is reachable only for path `"/"`, which no `/api/*`
request can be — so strictly *less* information is returned than the JSON
envelope did. `HX-Request` is attacker-controllable, but setting it only
swaps one 401 shape for another. No auth boundary moves.

**Kiosk containment (ADR-0020, coding-standards §10).** Enumerated every
htmx endpoint on the self-order surfaces: all nine are under
`/api/self-order/`, which `exempt()` returns on, so a walk-up customer can
never be bounced to the staff PIN keypad by this change.

**"No payment was taken" — verified, not taken on trust.**
`internal/pages/init.go:553` wraps the whole mux in
`recoverMiddleware(auth.Middleware(mux, authSvc))`, `/api/pos/tender` is
registered on that mux (`internal/pages/pos_api.go:1172`), and it is not
exempt — so the 401 is produced before the handler, hence before
`completeTender`. Client-side, `payments` and `pendingVoucherIssues` are
local arrays only and nothing server-side was mutated, so there is genuinely
nothing to roll back and the basket survives for after re-login. No redirect
loop: `/login` is exempt and serves the keypad. `finally { submitBtn.disabled
= false }` still runs on the early return, harmlessly.

**TDD red→green re-verified personally, twice.**

*Unit:* reverting only the `&& r.Header.Get("HX-Request") != "true"` clause
makes the new test fail with eight real assertion messages across all four
paths — a genuine failure, not a panic or a compile error:

```
auth_test.go:338: htmx /api/pos/hold without session = 401 hx-redirect="", want 401 + HX-Redirect:/login
auth_test.go:346: htmx /api/pos/hold 401 must not carry the JSON error body — htmx discards it
...(same for resume, scan, tender)
--- FAIL: TestMiddlewareHXRequestUnderAPIPathGetsRedirectNotJSON
```

Restoring the clause returns it to PASS.

*E2E — the spec is not a tautology:* with the same clause reverted, the real
Playwright/Chromium run **fails** at `session-expiry-redirect-2144.spec.ts:75`
(`page.waitForURL: Timeout 5000ms exceeded`). This also settles the spec's
own race caveat: the only background polls on the sale screen are
`/ui/pairing-notice`, `/ui/sync-chip` and `/ui/fiscal-chip`, all on **30s**
triggers and all on non-`/api/` paths, so none can win inside the 5s
assertion window — the documented race is real in principle but effectively
unreachable, and it certainly cannot rescue a reverted fix. Restored → passes.

**Real e2e run (not just a read).** `npx playwright test --project=auth`:
20/20 passed, including the new spec, which sorts last in file order exactly
as `playwright.config.ts`'s own ordering comment requires. The auth till
boots from a fresh `mktemp -d` data dir per run, so this was a genuine
first-boot wizard + PIN login + real session each time.

**Manual (`web/help/`) — correctly untouched, checked rather than assumed.**
`web/help/en/users.md:44` already reads: "The till locks itself back to the
PIN pad after it sits untouched for a while, no action or transaction lost:
whatever was in the basket is exactly as you left it once you (or anyone
else allowed to) sign back in." The manual already described the intended
behaviour — the bug was the *product* failing to match it. This fix brings
the product into line with the shipped manual, so no topic prose, no steps
and no screenshot content change. (The screenshot *manifest* still needed
regenerating; that is F2, a hash-freshness matter, not a pixel one.)

**Recurring-bug-class sweep.** No file is written without `os.MkdirAll`-ing
its directory and no cwd-relative path is used where `paths.Data(...)`
belongs — the diff contains no file I/O at all. No real client or shop name
appears as demo/seed/test data (the spec introduces only `'Hold Sale'` and
the barcode `'0000000000000'`; `ensureOperator`'s pre-existing `'Demo Shop'`
is untouched).

## Gate

Everything below run in the review worktree after the fixes.

| Check | Result |
| --- | --- |
| `gofmt -l .` | no output |
| `go build ./...` | exit 0 |
| `go vet ./...` | exit 0 |
| `go test ./...` | **57 packages ok, 0 FAIL**, exit 0 |
| `golangci-lint run ./...` | `0 issues.` |
| `guard-i18n.sh` | ✓ 1653 template keys resolve; all locales match en.json |
| `guard-data-access.sh` | ✓ no inline SQL outside `internal/data` / `internal/db` |
| `guard-page-http-error.sh` | ✓ no bare `http.Error` in a page-route handler |
| `guard-docs-shots.sh` | ✓ 31 topics × 4 locales fresh (`surface 0b67ca747e6e…`) — after F2 |
| `guard-e2e-fixtures-import.sh` | ✓ 115 specs checked — after F1; self-test green |
| all other `ci.yml` `build`-job guards | ✓ (25 guards run individually, all exit 0) |
| `shellcheck` on the edited guard | clean |
| `playwright --project=auth` | 20/20 passed |

`shellcheck` is not preinstalled in this worktree; it was installed locally
at 0.11.0 while CI pins 0.9.0 (`guard-shellcheck-version.sh`), so the local
run is indicative rather than authoritative. The edited script is clean at
0.11.0, and the edit adds only comment lines plus one quoted array element
in the same form as the three existing ones. The four `SC2329` infos 0.11.0
reports elsewhere under `scripts/ci/` are pre-existing, in files this card
does not touch, and invisible to CI's pinned 0.9.0 — deliberately not
"fixed" here.

## Deferred / accepted

- **`resume` and `tender` are not driven by a literal UI button click** in
  the e2e spec — only by a real same-origin HTTP request carrying the
  identical `HX-Request` header htmx sends, against the real running binary
  and a really-revoked session. Seeding a catalog plus a held order into the
  bare first-boot `auth` till to get a clickable Resume and a non-empty
  basket was judged disproportionate for this card. Both paths are covered
  by real clicks' equivalent at the Go level in
  `TestMiddlewareHXRequestUnderAPIPathGetsRedirectNotJSON`. Stated in the
  spec's own header so it is not overclaimed.
- **F5** (case-sensitive `HX-Request` compare) accepted as-is; consistent
  with the pre-existing sibling line.
- **F6** (raw-`fetch` `/api/*` callers on admin pages still rendering the
  JSON envelope on an expired session) left out of scope; recommend a
  follow-up card.

## Verdict

**Safe to merge.** The fix is correct, minimal and sits at the right layer;
the unit test and the e2e spec are both genuinely fix-sensitive (personally
re-verified by reverting the fix and watching each go red); no auth boundary
moves and strictly less information is returned than before; kiosk
containment and the `exempt`/`optionalAuth` tiers are untouched. The two
CI-blocking guard failures the branch shipped with (F1, F2) are fixed in the
worktree and re-verified green, as are the two test-quality findings (F3,
F4).
