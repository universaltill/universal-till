# Code review — HTTP panic-recovery middleware for internal/pages (ut-docs#1271)

- **Date:** 2026-08-29
- **Branch:** `feat/1271-panic-recovery-middleware` (commit `f867182`)
- **Reviewer:** independent reviewer (Opus, fresh context — did not write the
  code), read-only pass over the finished diff plus real gate runs.
- **Verdict at first pass: NOT SAFE TO MERGE AS-IS — 1 blocking, 1
  recommended-before-merge.** The design and the tests were sound; the
  blocker was a CI-blocking guard the diff turned red (`guard-docs-shots.sh`),
  and the medium finding was a real behaviour regression for streaming
  responses (already-started responses got a second header + an appended
  error body).
- **Update, same day — all required actions applied, re-verified green:**
  - `make docs-shots` run (twice — once after the initial diff, again after
    the fixes below changed `recovery.go`'s content and re-dirtied the
    surface hash); `web/help/img/manifest.json` + the churned PNGs committed.
    `guard-docs-shots.sh` now passes (`23 routed topics × 4 locales
    screenshotted and fresh`).
  - Finding 2 (mid-stream response corruption) fixed: `recoverMiddleware` now
    wraps `w` in `recoveryResponseWriter`, which tracks whether
    `WriteHeader`/`Write` already ran; the recover block returns without
    writing anything when the response was already committed, instead of
    appending a second header/error body. New test
    `TestRecoverMiddleware_MidStreamPanicDoesNotCorruptCommittedResponse`
    pins the exact CSV-mid-stream-panic scenario the review reproduced;
    mutation-verified (reverting the `wroteHeader` guard makes that test fail
    with `Content-Type = "application/json", want text/csv`, confirming it's
    a real pin, not a tautology).
  - Optional Finding 4 (`http.ErrAbortHandler` not re-panicked) also applied:
    the recover block now re-panics that sentinel before doing anything else,
    per `net/http`'s own convention. New test
    `TestRecoverMiddleware_RepanicsErrAbortHandler` pins it.
  - Optional Finding 3 (full stack in the Problems ring) deliberately **not**
    applied — see "Declined follow-up" below.
  - Full gate re-run after all fixes: `gofmt -l .` empty, `go build ./...`,
    `go vet ./...`, `go test ./...` (whole repo) all green, all 5
    `recovery_test.go` cases green, all four CLAUDE.md-listed guards touched
    by this diff (`guard-data-access.sh`, `guard-kiosk-engine.sh`,
    `guard-i18n.sh`, `guard-docs-shots.sh`) green.
  - This update was written by the same session that applied the fixes
    (Sonnet, the card's Dev tier), not by a second independent review pass —
    per the pipeline's "one review round unless the first finds a
    blocker-class issue, scoped to the fix" rule, a fresh Opus round was not
    re-spent re-reviewing the whole diff for these two contained, mechanical
    fixes. The mutation-tested new tests above are what stand in for that.

## What shipped

`internal/pages` had no general HTTP panic recovery — the only `recover()` in
the tree is `sync_admin.go`'s `syncPullPlugins`, scoped to one background loop.
An unexpected handler panic therefore killed the connection with no response at
all, which on the Android till is indistinguishable from a network failure.

- **New `internal/pages/recovery.go`** — `recoverMiddleware(next http.Handler)`:
  `defer`/`recover`, logs `panic recovered: %v\n%s` (value + `debug.Stack()`)
  through `internal/logging`, then answers the client with a localized generic
  500: the repo-standard `{"data":null,"error":{code,message}}` JSON envelope
  for `/api/*` paths (mirroring `auth.Middleware`'s own api-vs-page split),
  `http.Error` with localized plain text for everything else. Locale from
  `httpx.ResolveLocale`, message from the new `common.error.server` key.
- **`internal/pages/init.go`** — both `Init()` return paths now wrap the final
  handler, `recoverMiddleware` outermost (wrapping `auth.Middleware` itself, not
  just `mux`), including the `UT_AUTH=off` path.
- **`web/locales/{en,ar,fa,tr}.json`** — new `common.error.server`.
- **New `internal/pages/recovery_test.go`** — 3 tests: API-path JSON shape,
  non-API localized plain text, pass-through when no panic.

## Independent review — what was actually run (not read)

All commands run in `/home/user/universal-till` at `f867182`:

- `gofmt -l .` (empty), `go build ./...`, `go vet ./internal/pages/` — clean.
- `go test ./internal/pages/... ./internal/httpx/... ./internal/app/...` — all
  `ok` (the `pages` package takes 115s; the panic stack printed during the
  recovery tests is the middleware's own log line, expected).
- **Every guard in `.github/workflows/ci.yml`'s `build` job**, 18 scripts:
  all green **except `guard-docs-shots.sh` (exit 1)** — see Finding 1.
- Locale key sets compared programmatically across all four files: 1754 keys
  each, no drift; the ar/fa/tr `common.error.server` values are byte-identical
  to the strings those locales already use for the same English sentence under
  `basket.error.server` et al (each locale has exactly one distinct
  `*.error.server` value) — the claimed reuse is real, not an approximate
  re-translation.

### TDD claim re-verified personally (and pushed further than the claim)

The dev's claim — revert `recovery.go` + the `init.go` wiring, get a compile
failure, restore to green — **reproduced exactly**:
`internal/pages/recovery_test.go:34/71/97: undefined: recoverMiddleware`,
`[build failed]`; restored → `ok`. That claim is true but weak on its own
(any new-symbol test compiles-fails), so the tests were additionally
**mutation-tested** against three deliberately-wrong implementations, each
restored immediately afterwards in the same shell invocation:

1. Middleware reduced to a pass-through (recover removed) → tests **FAIL**
   (panic escapes into the test binary).
2. Recovers correctly but returns the raw key instead of a translation →
   **FAIL** on both localized assertions (`error.message = "common.error.server"`,
   `body = "common.error.server\n"`).
3. Recovers but drops the api-vs-page split (plain text everywhere) → **FAIL**
   (`Content-Type = "text/plain; charset=utf-8", want application/json`).

So the tests are real pins on all three behaviours the card asked for, not
false-pass shells. Working tree restored and byte-compared against pre-mutation
backups (`RESTORED_CLEAN`), tree clean afterwards.

> **Process note:** a commit (`f867182`) landed on the branch *during* this
> review — the stop-hook case the `reviewer` skill warns about (ut-docs#386).
> Its content was checked line-by-line against the reviewed working tree: it
> captured the correct, un-mutated files (commit timestamp 15:11:32 precedes
> the first mutation write), and the author is
> `Farshid Mirza <4035824+farshidmirza@users.noreply.github.com>`, not an
> AI-tool default. No harm done, but the mutation work should have run in an
> isolated worktree; noting it so the next cycle does.

## Findings

### 1. BLOCKING — this diff turns `guard-docs-shots.sh` red (CI `build` job fails)

```
guard-docs-shots: the app surface (web/ui/**, web/public/**, or internal/pages/**.go)
changed since the manual's screenshots were last taken
```

Verified it is this diff and not pre-existing: a throwaway worktree at the base
commit `ca2b83b` runs the same guard green
(`23 routed topics × 4 locales screenshotted and fresh (surface 2cffc8933439…)`).

Cause is the guard's documented, deliberate over-inclusion: its `surface_sha256`
hashes **every non-test `internal/pages/**.go`**, excluding only a file whose
registered routes are all unscreenshotted. `recovery.go` registers *zero* routes,
which the guard keeps on purpose (a zero-route file can still feed a
screenshotted page's data), and `init.go` changed as well. No pixel actually
changes here — but the guard cannot know that, and CI is the gate.

**Remedy:** `make docs-shots` and commit the regenerated
`web/help/img/manifest.json` (plus whatever PNG-encoder jitter the rerun
produces — same situation the 2026-08-29 ut-docs#1273 record documents).
Chromium is present (`/root/.cache/ms-playwright`, `/opt/pw-browsers`), so
`e2e/scripts/docs-shots.sh` should take the pre-installed-browser path. No
manual *prose* needs changing (no new route — `guard-help-topics.sh` is green —
and no screen a shop owner navigates to was altered).

### 2. MEDIUM — recommended before merge: a response that already started gets a second header and an appended error body

The middleware writes unconditionally in the deferred function; it never checks
whether the handler already wrote. For handlers that stream — and this codebase
has several that set headers and an explicit `200` *before* writing rows:
`reports_page.go:812-815` (worker-allocations CSV), `eod_api.go:1032`,
`invoice_page.go:385`, `audit_page.go:117`, `import_page.go:1135` — a panic
mid-stream is now silently papered over.

Reproduced against a real `httptest` server with the same middleware shape and a
CSV-style handler that panics mid-stream (nil-map write):

- **With the middleware:** `status=200`, `content-type="text/csv"`,
  `content-length="139"`, body =
  `date,worker,amount\n2026-08-29,alice,500\n{"data":null,"error":{...}}` —
  plus `http: superfluous response.WriteHeader call` in the server log. The
  operator downloads an export that *looks* successful and has a corrupt
  trailing line.
- **Without the middleware (today's behaviour):** the client gets `EOF` — a
  visibly failed download.

For an accounting/export surface on a POS, "quietly truncated and corrupted, but
200 OK" is worse than "obviously failed". Same class applies to `/api/*`
(envelope appended to partial JSON) and to any page already partially written.

**Suggested fix (small):** wrap `w` in a tiny writer that records whether
`WriteHeader`/`Write` has happened (forwarding `http.Flusher`, and `Hijacker`/
`ReaderFrom` if ever needed) and, when the response has already started, log and
return without writing — letting the connection abort, which is the honest
signal. The three existing tests keep passing; add a fourth for the
already-written case.

### 3. LOW — the full `debug.Stack()` goes into the shop-owner-visible Problems ring

`logging.L().Errorf` at `Error` level feeds `logging.remember` → the process-wide
Problems ring, which surfaces in three places:

- `backoffice_page.go:57` takes the newest 5 and `backoffice.html:38` renders
  `{{ .Msg }}` into a single `<td>` — a multi-KB goroutine dump in a table cell
  on the shop owner's back-office home. HTML-escaped by `html/template`, so no
  XSS, but the panel layout is wrecked, and a *repeating* panic (a polling
  endpoint, a kiosk fragment) evicts all 50 real one-line problems.
- `cloudsync_wire.go:382-392` truncates each message to 200 bytes, so only the
  head reaches the cloud heartbeat — the panic value survives, the stack mostly
  doesn't. Good.
- `issuereport/bundle.go:174` carries `logging.Recent()` in full — intended
  debug capture, fine.

**Suggestion:** keep the panic *value* in the `Errorf` message (that's the part
worth having in the Problems feed) and truncate the stack to the first N frames,
or log it as a separate line, so the operator-facing panel stays readable.

### 4. LOW — `http.ErrAbortHandler` is not re-panicked

`net/http`'s convention: a handler panicking with `http.ErrAbortHandler` means
"abort this response silently", and recovery middleware is expected to re-panic
it rather than write a 500. Nothing in this repo panics with it today (no
`httputil.ReverseProxy`, no `Hijack`, verified by grep), so this is latent only —
but a one-line `if rec == http.ErrAbortHandler { panic(rec) }` future-proofs it
cheaply, and `registerExternalProxy` is exactly the kind of surface that could
grow one.

### 5. Explicitly checked, no problem found

- **Is logging the raw panic value safe?** The *client* never sees it: the
  response body is only the generic localized string — no stack, no panic value,
  no internal path leaks. On the log side, `%v` of the panic value could carry
  request-derived data if some handler ever panicked with a value built from
  input; no such call site exists today (the nearest thing, `httpx.Render`'s
  `template.Must`, panics with template parse errors — file paths only), and Go
  stack frames print word-sized argument values, not string contents. The
  standing rule to keep in mind: never `panic(err)` with a PIN/token inside,
  because that value now reaches the Problems panel and the first 200 bytes of
  the cloud heartbeat.
- **Does wrapping OUTSIDE `auth.Middleware` weaken auth?** No. `recover()` does
  not re-enter `next`, so there is no fail-open path; the request is never
  modified; auth-exempt routes (`/self-order`, `/api/self-order/*`, `/o/{token}`)
  and auth-required routes get byte-identical 500s, so the response leaks no
  auth-state information; and a panic *inside* auth resolution now yields a clean
  500 instead of a dropped connection, which is strictly better. `Init()`'s
  `UT_AUTH=off` path is wrapped too — both return statements, verified.
  `guard-kiosk-engine.sh` is green: `recovery.go` registers no routes and never
  touches `Deps.Engine`.
- **Offline-first:** no network dependency introduced; nothing in the checkout
  path is blocked; no modal added.
- **Repo conventions:** JSON envelope matches `auth.Middleware`'s precedent and
  the CLAUDE.md contract; `internal_error` is snake_case and consistent with the
  existing `unauthorized`/`not_entitled` codes; no SQL outside `internal/data`;
  no money handling; no hardcoded user-facing string (`guard-i18n.sh` green).
- **Two recurring pipeline bug classes:** no file writes at all (so no missing
  `os.MkdirAll`), and no cwd-relative path where `paths.Data(...)` belongs — the
  only path use is the test's `filepath.Join("web","locales")` after the
  package's existing `chdirRoot(t)` helper, which is the established
  test-support pattern here.
- **No real client/shop name** and **no secret-shaped literal** anywhere in the
  diff (`panic("boom")`, `/api/whatever`, `/designer`).
- **ADR: agreed, none needed.** No accepted ADR governs HTTP error handling or
  panic policy (grepped `ut-docs/adr/`), nothing here contradicts ADR-0008
  (server-rendered HTMX, no SPA) or the offline-first rules, and no architecture
  doc describes the middleware chain in a way this makes stale.
- **Test hygiene:** `initRecoveryTestI18n` duplicates the per-file i18n bootstrap
  other tests in this package already inline (`backup_api_test.go`,
  `help_hint_test.go`) and sets the `"en"` default the rest of the package
  assumes (`kitchen_print_test.go` explicitly restores `"en"` after its own
  locale switches), so no cross-test ordering hazard is introduced. Nit only.

## Deferred / follow-up (not fixed here)

- **Goroutine panics remain fatal.** This middleware only covers the request
  goroutine; a panic in a handler-spawned goroutine or in any of the background
  loops (`StartSyncPush`, `StartCloudSync`, the schedulers) still takes the
  process down. Genuinely out of this card's scope, but it is the larger
  remaining exposure on an unattended till — worth a backlog card.
- **The 500 is unstyled plain text.** For a full-page navigation on the
  self-order kiosk (no browser chrome, no back button) that is a dead end for the
  customer; and for HTMX fragment requests htmx does not swap non-2xx responses
  by default, so a panic on a fragment stays invisible unless that page happens
  to have its own `htmx:responseError` handler (several do, most do not). Not a
  regression — before this change nothing rendered at all — but "the operator now
  sees an error" only really holds for full-page loads and API callers. A minimal
  styled error page with a way back would finish the job.
- **Locale key placement:** `common.error.server` was appended at file end rather
  than beside the other `common.error.*` keys (en.json:1537-1538). Cosmetic, and
  consistent with how recent keys have been appended; not worth a churn commit.

## Summary of required actions before merge

1. Run `make docs-shots`, commit the regenerated manifest/screenshots (blocking:
   CI `build` job is red without it).
2. Recommended: guard the already-started-response case (Finding 2) before this
   ships, so a panic mid-export cannot produce a 200 with a corrupted body.
3. Optional one-liners: re-panic `http.ErrAbortHandler`; truncate the stack in
   the Problems-ring message.
