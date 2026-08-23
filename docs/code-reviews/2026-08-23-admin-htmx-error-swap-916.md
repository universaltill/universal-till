# Code review: admin htmx handlers silently discard their own error notices

**Card:** universaltill/ut-docs#916
**Repo/branch:** `universal-till`, `fix/916-admin-htmx-error-swap`
**Build model:** Sonnet (complexity: medium, inline). **Reviewer:** independent
Opus subagent, isolated worktree, different model from the Sonnet session that
implemented it.

## What shipped

htmx never swaps a non-2xx response into its target by default — it fires
`htmx:responseError` and discards the body instead. Several admin-page
handlers that fail (`print/labels`, `print/test`, `print/receipt` reprint,
invoice issue, backup now/restore, sync join/promote) render a real,
translated `<span class="muted">✗ …</span>` error fragment straight into
their own `hx-target`, but with no fix that fragment was silently discarded
and the operator saw nothing at all — there's no `#pos-alert`-equivalent
fallback banner on these pages the way the sale screen has one.

`web/public/app.js`'s existing `htmx:beforeSwap` listener (previously scoped
narrowly to a 400-status `/api/pos/` basket-fragment carve-out from
ut-docs#213) is generalized to force the swap for admin-page responses,
while leaving the sale-screen carve-out's own behavior unchanged.

## Independent review — round 1 (first version of the fix)

Full independent pass (isolated worktree, Opus, no access to the
implementer's reasoning): read the diff, searched `internal/pages/*.go` for
counter-examples, ran the new spec against HEAD and against the pre-fix
commit to confirm the TDD claim was real (not tautological), re-ran
`sale-screen-213.spec.ts` for regressions, ran `gofmt`/`go build`/`go vet`,
and checked i18n/self-order-isolation scope.

**Blocking, found and empirically reproduced** (the first version of this
fix force-swapped **any** non-2xx response, not just the `.muted` fragment
convention ut-docs#916 actually targets):

1. **A plain `http.Error(...)` response is `text/plain`**, and htmx's
   fragment parser yields zero nodes for a plaintext body against an
   `outerHTML`/`innerHTML` target — force-swapping it **wipes the target**
   instead of showing anything. Reproduced live: a 400 from
   `/api/catalog/variant` into `#catalog-variants` (`outerHTML`) destroyed
   the entire variants/barcodes/modifier-groups editor panel and still
   showed the operator nothing — strictly worse than the original bug.
   Same shape at several other `catalog_variants.html`/`tax_codes_table.html`
   forms, plus two self-destructing 15s/pollers (`orders_list.html`,
   `pending_pairings.html`).
2. **Forcing `isError = false` unconditionally silences already-working
   `htmx:responseError` handlers** on `refund.html` and `catalog.html`'s
   item form, which already show their own errors correctly today.
3. **Forcing `isError = false` also flips `detail.successful` to `true` on
   a real failure**, inverting `hx-on::after-request` branches keyed off
   it — reproduced live: a real 400 from `/api/settings/ui-scale` was
   reported as `successful: true`, so `settings.html`'s save handlers
   would reload the page **as though a rejected save had succeeded**,
   masking the failure rather than showing it. Same shape across
   theme/OSK/display-mode/window-mode/launch-on-startup and
   `pending_pairings.html`'s wrong-PIN error.
4. Raw `err.Error()` / untranslated developer strings can reach the
   operator's screen once such a response is force-swapped (pre-existing
   `internal/pages/common/errors.go`'s own stated policy against this) —
   noted as a systemic follow-up, not blocking this fix once (1)–(3) are
   addressed, since the Content-Type discriminator below also closes this
   specific path (a `text/plain` `http.Error` body is never swapped).

**Non-blocking**: `helpers.ts`'s new `watchConsole(page, extraExempt)`
parameter is a reasonable, appropriately-scoped generalization (unchanged
default behavior, single-test-scoped 502 exemption); self-order is
unaffected (`app.js` isn't loaded there); `docs/sale-screen-notifications.md`
should be refreshed for the widened carve-out description (follow-up, not
blocking this card's scope).

## Fix (this round)

Reworked the swap condition from "swap any admin-page non-2xx" to
**"swap only a real, non-empty `text/html` body"** — the exact
discriminator the reviewer proposed and independently verified themselves
before handing the finding back:

```js
var contentType = (d.xhr.getResponseHeader && d.xhr.getResponseHeader('Content-Type')) || '';
if (contentType.indexOf('text/html') === -1) return;
if (typeof d.serverResponse !== 'string' || d.serverResponse.trim() === '') return;
d.shouldSwap = true;
d.isError = false;
```

This is a clean separation: every handler ut-docs#916 actually targets sets
`Content-Type: text/html` before writing its fragment (`print_api.go`,
`backup_api.go`, `sync_api.go`, `invoice_page.go`); `http.Error` always
answers `text/plain`, so it's never swapped and falls through to
`htmx:responseError` exactly as it did before this whole fix — resolving
findings 1–4 by construction, not by patching each call site individually.

## Verification (personally re-run, not just re-reading the reviewer's report)

- **Findings 1–3 individually re-reproduced against the corrected fix**:
  - `#catalog-variants` after a plaintext 400 (`/api/catalog/barcode/delete`,
    no `barcode`) — panel intact, real content, count 1 (was 0/destroyed).
  - `/api/settings/ui-scale` with an invalid scale — `detail.successful`
    correctly `false` (was incorrectly `true`).
- **TDD, both directions**: `e2e/tests/htmx-admin-error-swap-916.spec.ts`'s
  regression spec (plaintext-error-must-not-wipe-target) fails against the
  first (blocking) version of the fix and passes against the corrected one;
  confirmed by mechanically swapping the swap-condition block back and
  forth and re-running.
- Two "happy path" specs (print/test 502, print/labels 404) still pass —
  the fix still does what ut-docs#916 asked for.
- **Full e2e suite**, `--project=default`, 154 specs: all green except one
  pre-existing failure in `catalog-image-to-till.spec.ts` (an image-load
  timing assertion), independently confirmed present on unmodified
  `main` and unrelated to this diff (same failure, same assertion, files
  this diff never touches).
- `sale-screen-213.spec.ts`: all 7 specs green — the `/api/pos/` carve-out
  is provably untouched.
- `gofmt -l .` clean, `go build ./...` clean, `go test ./...` clean (this
  diff touches no Go source).
- `guard-i18n.sh`, `guard-htmx-loaded.sh`, `guard-data-access.sh`,
  `guard-kiosk-engine.sh`, `guard-compliance-claims.sh`,
  `guard-plugin-menu-read.sh` all pass — no new user-facing strings
  introduced (the fix reuses server-rendered, already-translated
  fragments), no Go/template changes to trip the other guards.

## Safe-to-merge verdict

**Yes**, after the Content-Type-based correction. The first version's
blanket "swap any non-2xx" policy was a genuine, reproducible regression
risk (data-destroying swaps and false-success reporting on real admin
pages) — caught by independent review before merge, exactly as this
process is meant to. The corrected version is scoped precisely to the
class of response the card's own bug report described, verified not to
regress the pre-existing sale-screen carve-out, and re-tested end-to-end.

## Non-blocking follow-ups (not this card's scope)

- Refresh `docs/sale-screen-notifications.md`'s description of the
  carve-out to mention the new admin-page swap path.
- `print_api.go:414` still appends a raw `err.Error()` into a translated
  fragment (pre-existing, same class as `ut-docs#316`'s stated policy) —
  now more visible since its fragment reliably reaches the screen; worth
  its own card.
- The systemic use of `http.Error(w, err.Error(), …)` across ~30
  `internal/pages/*.go` files (raw/untranslated error text) is a
  pre-existing, unrelated i18n/security-adjacent gap outside this card's
  scope — `ut-docs#921`-style follow-up territory, not filed here to avoid
  scope creep on an already-large audit.
