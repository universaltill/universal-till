# Review: /items and /admin panel swaps keep document.title in sync (ut-docs#2162)

Independent review — reviewer had no access to prior design/dev discussion,
only the diff (commit `4a05c9a2`, branch
`fix/2162-panel-swap-stale-document-title`) and this repo's own rules.

## What shipped

`web/ui/layouts/base.html`'s `<title>{{ .title }}</title>` only ever renders
on a full-page response; an in-panel htmx swap on `/items`, `/admin` (and
`/help/{topic}`, for free) is a bare fragment that never carried title
information at all, so the browser tab kept showing whichever section's
title was current when the shell first loaded.

- `internal/httpx.RenderContentFragment`, `RenderPartial`, and `RenderWith`'s
  `name == "content"` case now set a response header (`X-UT-Page-Title`)
  from the template data's `"title"` field, percent-encoded so non-ASCII
  (ar/fa/tr locale) titles survive the Latin-1 decoding `getResponseHeader()`
  does.
- A new `htmx:afterSwap` listener in `web/public/app.js` reads the header
  back and sets `document.title`, scoped to an explicit swap-target
  allowlist (`items-panel`, `admin-panel`, `manual-panel`) so `/import`'s
  modal — which also renders through `RenderContentFragment` — doesn't
  wrongly retitle the tab while just a dialog is open over the real panel.
- New unit tests in `internal/httpx/httpx_test.go`, and a new Playwright spec
  `e2e/tests/items-admin-panel-title-2162.spec.ts`.
- No template files touched; the fix lives entirely in the three Go choke
  points every fragment-capable handler already funnels through.

## What I independently checked

All commands run from this worktree at commit `4a05c9a2` (reset from the
worktree's original branch tip onto that commit — see note below).

| Command | Result |
|---|---|
| `gofmt -l .` | no output (clean) |
| `go build ./...` | exit 0 |
| `go vet ./...` | exit 0 |
| `golangci-lint run ./internal/httpx/...` | `0 issues.` |
| `golangci-lint run ./...` (full repo, post-fix) | `0 issues.` |
| `go test ./internal/httpx/...` | `ok` (0.36s), all new + existing tests pass |
| `go test ./internal/pages/...` | `ok` (~200s), all green, both before and after my fix |
| `go test ./...` (full repo, post-fix) | `ok` — see full output note below |
| `bash scripts/ci/guard-i18n.sh` | ✓ pass |
| `bash scripts/ci/guard-e2e-fixtures-import.sh` | ✓ pass |
| `bash scripts/ci/guard-htmx-loaded.sh` | ✓ pass |
| `bash scripts/ci/guard-data-access.sh` | ✓ pass |
| `bash scripts/ci/guard-help-topics.sh` | ✓ pass (no new routes; N/A but ran it anyway) |
| `bash scripts/ci/guard-compliance-claims.sh` | ✓ pass |
| `cd e2e && npx playwright test --project=default tests/items-admin-panel-title-2162.spec.ts` | **4 passed** (12.5s), against a real Go server (`run-till.sh`) |
| `shellcheck scripts/ci/*.sh` | not available in this sandbox (`command not found`) — not exercised; no shell scripts were touched by this diff so this gate is not expected to be affected |

**Setup note**: the assigned worktree's checked-out branch did not itself
carry commit `4a05c9a2` (that branch lives in a sibling worktree); I ran
`git reset --hard 4a05c9a2` inside this worktree only, bringing its own
branch pointer to the same commit content, per the task's description of
the intended starting state. Nothing outside this worktree was touched.

## My own TDD re-verification

Picked `TestRenderWithContentSetsPageTitleHeader` (pins the `/catalog`
`RenderWith`-based fragment path — the gap the original PR's tester found).
Commented out the `if name == "content" { writePageTitleHeader(w, data) }`
block in `internal/httpx/httpx.go` and re-ran just that test:

```
=== RUN   TestRenderWithContentSetsPageTitleHeader
    httpx_test.go:266: X-UT-Page-Title = "", want "Catalog"
--- FAIL: TestRenderWithContentSetsPageTitleHeader (0.00s)
FAIL
FAIL	github.com/universaltill/universal-till/internal/httpx	0.007s
```

A genuine assertion mismatch, not a compile error — confirms the test
actually exercises the fix. Restored with `git checkout -- internal/httpx/httpx.go`
and re-ran:

```
=== RUN   TestRenderWithContentSetsPageTitleHeader
--- PASS: TestRenderWithContentSetsPageTitleHeader (0.00s)
PASS
ok  	github.com/universaltill/universal-till/internal/httpx	0.006s
```

## Findings

### 1. FIXED (Medium/High) — percent-encoding used the wrong escaping, breaking every multi-word title

`writePageTitleHeader` used `net/url.QueryEscape`, which follows
`application/x-www-form-urlencoded` and encodes a space as `+` — a
convention only `url.QueryUnescape` (or a form/query decoder) reverses.
The paired client-side call, `decodeURIComponent` in
`web/public/app.js`'s `htmx:afterSwap` listener, only ever unescapes
`%XX` sequences; it does **not** turn a literal `+` back into a space
(confirmed live: `node -e 'console.log(decodeURIComponent("Country+settings"))'`
prints `Country+settings`, not `Country settings`).

This is not a hypothetical edge case — most of the real "title" values this
diff wires up contain a space: `country_settings_page.go` ("Country
settings"), `fiscal_register_page.go` ("Fiscal register"),
`fiscal_device_page.go` ("Fiscal device"), `tax_codes_page.go` ("Tax
codes"), and `catalog/handlers.go`'s "Customization options" / "Option
sets". Every one of those tab titles would have rendered with a literal
`+` where the space should be. The original tests didn't catch it because
the one non-ASCII test case used a single Farsi word with no space in it.

**Fix applied**: switched `writePageTitleHeader` to `net/url.PathEscape`,
which encodes a space as `%20` (which `decodeURIComponent` does reverse
correctly) and still percent-encodes control characters (CR/LF included),
so the no-header-injection property is unchanged. Verified manually:

```
orig="Country settings" QueryEscape="Country+settings" PathEscape="Country%20settings"
orig="line1\r\nline2\tX" PathEscape="line1%0D%0Aline2%09X"
```

Added `TestRenderPartialTitleWithSpaceRoundTripsForClientDecoding` to
`internal/httpx/httpx_test.go`, which fails loudly (`contains a literal
"+"...`) against the original `QueryEscape` code and passes against the
`PathEscape` fix — re-verified with the same revert/restore procedure as
above. Also switched the existing
`TestRenderPartialSetsPageTitleHeaderWhenPresent` to decode with
`url.PathUnescape` instead of `url.QueryUnescape`, since that's the
Go-side call whose semantics actually match the client's
`decodeURIComponent` (a literal `+` untouched) rather than `QueryUnescape`
(which would silently paper over exactly this bug in the test itself).

### 2. Checked, no bug — allowlist completeness

Cross-checked every `RenderContentFragment(`/`RenderWith(...)("content"...)`
call site under `internal/pages/**` against its template's actual
`hx-target`:

- `items.html` → `#items-panel` (allowlisted)
- `admin.html` → `#admin-panel` (allowlisted)
- `help.html`/`help_nav.html`/`help_results.html` → `#manual-panel`
  (allowlisted — the "for free" case the commit message mentions)
- `catalog.html`'s import button → `#import-modal` (correctly **not**
  allowlisted — this is exactly the case the allowlist exists to exclude)
- `setup.html` (via `RenderPartial`) → never targets any of the three
  panel ids; the header is a harmless no-op client-side there.

No other template anywhere in `web/ui` declares an element with
`id="items-panel"`, `id="admin-panel"`, or `id="manual-panel"`, so there's
no accidental-match risk either. Allowlist is complete and correct as of
this diff.

### 3. Checked, no bug — double-set / sub-request leakage

`/items` and `/admin`'s own bare-GET handlers (`items_page.go`,
`admin_page.go`) build their default panel content via
`embedItemsSection`/`embedAdminSection`, which replay the request through
the mux into a **separate** `httptest.NewRecorder()` and only read back
`rec.Body`. Any `X-UT-Page-Title` header the inner section handler sets
lands on that throwaway recorder, never on the real `http.ResponseWriter`
of the outer full-page response — and the outer response renders via
`httpx.Render`, not `RenderWith("content", ...)`, so it never calls
`writePageTitleHeader` itself either. No double-set, no leakage.

### 4. Checked, no bug — `data any` type-assertion safety

`writePageTitleHeader`'s `data.(map[string]any)` assertion is the safe
two-result form; a `nil` (`issue_report_page.go`'s
`RenderPartial("ui/partials/bugreport_chip.html", nil)`) or a struct type
(`catalog/handlers.go`'s `modifierAdminItem{...}`, `order_tracking.go`'s
view-data structs) both fail the assertion cleanly and return without a
panic. Confirmed every real `RenderWith(...)("content", ...)` and
`RenderContentFragment`/`RenderPartial` call site with a `"title"` key
passes a genuine `map[string]any`.

### 5. Checked, no bug — header-injection / trust of "title" values

Every `"title"` value feeding this path is either a fixed English string
literal (`"Items"`, `"Administration"`, `"Country settings"`, etc.) or
`help_page.go`'s manual-topic title, which comes from `web/help/**`
content shipped in the binary (author-controlled, not runtime
user/plugin input). Neither is attacker-controlled. Independent of that,
`url.PathEscape` percent-encodes CR/LF and all control characters, so even
a hypothetically hostile title could not inject a header or split the
response.

### 6. N/A — file-write / cwd-relative-path bug classes

No file writes and no path handling in this diff at all (it's response
headers and a `document.title` assignment) — neither recurring bug class
applies.

### 7. N/A — real client/shop name in test data

None found; all values are literal English UI strings, a Farsi encoding
fixture, and one deliberately generic "Country settings" test string I
added myself.

### 8. N/A — i18n

No new hardcoded user-facing string. The `"title"` values consumed here
are pre-existing (this diff only reads them to set a header) and are not
rendered as new visible text; `guard-i18n.sh` passes unchanged.

### 9. N/A — manual/help docs

Nothing a shop owner sees or does changes — no new screen, no new action,
no altered step. This only keeps an already-visible browser tab title in
sync with an already-existing in-panel swap. `guard-help-topics.sh`
confirms no new page routes were added.

## Verdict

**Safe to merge** after the fix above. One real, user-visible bug
(percent-encoding scheme mismatch breaking every multi-word title) was
found, fixed, and independently re-verified with a failing-then-passing
test; every other checklist item was verified clean. Full gate (build,
vet, lint, `internal/httpx` + `internal/pages` + full `go test ./...`,
all CI guards named in the task, and a live e2e Playwright run) is green
post-fix.
