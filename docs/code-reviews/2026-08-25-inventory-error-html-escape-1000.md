# Code review: escape respond*Error HTML branches (ut-docs#1000)

**Branch:** `fix/1000-inventory-error-html-escape`
**Reviewer:** independent Sonnet subagent, fresh context (complexity:easy →
fresh-context Sonnet review, per scrum-master's model routing) · **Author:**
Sonnet (this pipeline cycle)

## What shipped

`internal/pages/inventory_api.go`'s `respondError`, `respondOverrideError`
and `respondReturnError` built their HTML-response branch as
`fmt.Sprintf("<div class='error'>%s</div>", message)` with no escaping.
`message` can carry a caller-supplied request field echoed back in a
validation error — concretely, `CreateReturn`'s `line_id %s not found in
original sale` embeds the request body's `line_id` verbatim — and
`web/public/app.js`'s `htmx:beforeSwap` handler force-swaps any non-2xx
`text/html` response into the page DOM, so an attacker-controlled `line_id`
containing `<script>`/`<img onerror=…>` would execute in an authenticated
operator's browser. Found during independent review of ut-docs#731, filed
as ut-docs#1000, picked up by this pipeline cycle.

- New shared helper `errorHTML(message string) string` —
  `fmt.Sprintf("<div class='error'>%s</div>", html.EscapeString(message))`
  — used by all three `respond*Error` HTML branches.
- Regression test `TestCreateReturn_LineIDErrorMessageIsHTMLEscaped`: posts
  a `<script>alert(1)</script>` `line_id` to `/api/inventory/return`
  without `Accept: application/json`, asserts the raw payload is absent
  and `&lt;script&gt;` is present in the response.
- Unit test `TestErrorHTML_EscapesMessage` on the new helper directly,
  covering the two call sites (`respondOverrideError`, and `respondError`'s
  own callers) the end-to-end test doesn't exercise.

## Independent review — first round found one in-scope gap, fixed

Fresh-context Sonnet reviewer (no visibility into this diff's authoring):
inspected all three fixed call sites in context, read the rest of the file
end-to-end for the same unescaped-interpolation shape, traced
`GetLowStock`'s error path into `internal/data`. Confirmed build/vet/gofmt
clean and the full `internal/pages/...` suite green.

**TDD re-verified independently, not taken on trust**: the reviewer
neutralized `errorHTML`'s escaping (keeping the rest of the fix intact)
and re-ran the two new tests — both failed with the raw payload reflected
unescaped, confirming they gate the regression rather than false-passing.

### Finding — fixed in this same round

**F1 (blocker, in-scope)**: `GetLowStock`'s own error branch (a fourth call
site in the same file) still built its HTML response as
`fmt.Sprintf("<div class='error'>%s</div>", err.Error())` — the identical
unescaped-interpolation shape, left behind. `err.Error()` here currently
wraps a SQL-layer error rather than directly echoing a request field, so it
wasn't independently reported as exploitable the way `line_id` was, but
it's the same bug shape in the same file the fix's own scope note ("check
whether other respond*Error helpers... have the same gap and fix them
together") already called out to sweep. One-line fix: route it through
`errorHTML(err.Error())` too. Added
`TestGetLowStock_HTMLError_UsesErrorHTMLHelper` (closes a *closed* `*sql.DB`
to force the error path deterministically, same technique as the existing
`TestGetLowStock_JSONError_UsesDataErrorEnvelope`) asserting the HTML body
is built by `errorHTML`, not a bespoke `fmt.Sprintf`.

### Findings — deliberately out of scope, filed as follow-ups

- **Stored-XSS-shaped gap, same file, different call site**: `GetLowStock`'s
  *success*-path table interpolates `item.Name`/`item.SKU`/
  `item.LocationName` (persisted catalog data, not an immediate request
  echo) with no escaping — a different vector (stored vs. reflected) than
  what ut-docs#1000 scoped. Filed as ut-docs#1019 rather than folded into
  this fix, to keep this PR's regression tests scoped to what it actually
  changed.
- `html.EscapeString` also escapes `'`/`"` in addition to the strictly
  necessary `&lt;`/`&gt;`/`&amp;` for element text content — noted by the
  reviewer as correct, standard-library behavior, not an issue.

## Verification beyond the reviewer's automated pass

- `gofmt -l .` — clean.
- `go build ./...` — clean.
- `go vet ./...` — clean.
- `go test ./internal/pages/...` (no `-race`, matches this repo's actual CI
  gate — `.github/workflows/ci.yml` never runs `go test` with `-race`) —
  green, 80s.
- `go test $(go list ./... | grep -v '/internal/plugins$')` and
  `go test -timeout 20m ./internal/plugins` (CI's own two-step split) —
  both green.
- `go test ./internal/pages/... -race` was also tried as extra rigor beyond
  what CI requires. It timed out at the default 10-minute per-package
  budget on an *unrelated* test (`TestStoreAPI_Download...`, plugin-store
  handlers, nothing to do with this diff) stuck mid-migration under
  `-race`'s overhead. Reproduced identically against unmodified `main`
  (stashing this diff first) — same package, same 600.1s timeout, same
  migration-stuck stack. Confirmed pre-environmental, not a regression this
  PR introduced, and consistent with the already-tracked flakiness in this
  package family (ut-docs#979, `internal/plugins`).
- All CI-blocking guards relevant to a Go-only, non-template change run and
  green: `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-i18n.sh`, `guard-compliance-claims.sh`,
  `guard-help-topics.sh`, `guard-webkit-version.sh`,
  `guard-kiosk-launch-flags.sh`, `guard-android-status-address.sh`,
  `guard-android-i18n.sh`, `guard-emoji-font.sh`, `guard-htmx-loaded.sh`,
  `guard-autofill-suppression.sh`, `check-brand-assets.sh`,
  `guard-makefile-version.sh`.
- No UI/template/locale change — nothing for `guard-docs-shots.sh` or the
  manual (`web/help/`) to pick up; this fix is invisible under normal
  operation (only changes what a validation-error response looks like when
  it happens to carry markup).

## Scope note

Deliberately did not widen this PR to ut-docs#1019's stored-XSS-shaped
finding — same-file, same missing-escape *class*, but a genuinely different
vector (persisted catalog data vs. an immediate request echo) with its own
regression-test shape. Filed separately per this pipeline's own guidance:
several honestly-scoped fixes beat one commit claiming more than it
verified.
