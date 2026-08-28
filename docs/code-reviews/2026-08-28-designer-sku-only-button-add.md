# Designer: SKU-only items can be added as shortcut buttons (ut-docs#1220)

**Date:** 2026-08-28
**Branch:** `fix/1220-designer-sku-only-button-add`
**Repo:** `universal-till`
**Reviewed commit:** `128a3d1c` (parent `23d43b28` = `main`)

## What shipped

Two defects, one visible symptom ("tap the search result, the dropdown just
closes, no tile, no error"), reproduced live on Pi5-1 v0.6.12:

1. **The add posted an empty code.** `SearchItemsForShortcuts`' WHERE clause
   already matched `i.sku LIKE ?`, so a barcode-less item (loose produce,
   services — and the reported Cappuccino, SKU `30005`) was *found*; but the
   SELECT never projected `i.sku`, so `ShortcutSearchResult.Barcode` was the
   only identifier reaching `SearchResult.AddVals()`, which posted
   `code=""`. `ButtonStore.Add` rejects an empty code with a 400.
   Fix: project `COALESCE(i.sku, '')` (`internal/data/pos_repo.go`), carry
   it through `ButtonStore.SearchItems`, and have `AddVals()` fall back to
   SKU when Barcode is empty (`internal/ui/buttons.go`).

2. **The 400 was invisible.** `ButtonsHTTP.Add` answered with
   `http.Error(w, err.Error(), 400)` — a raw Go error string, `text/plain`,
   which htmx never swaps into `hx-target` for a non-2xx — while the
   search-result button's `hx-on` hid the dropdown on `htmx:afterRequest`
   *unconditionally*, so a rejected add looked exactly like a successful
   one. Fix: `Add` now writes a localized `<div class="error">` fragment
   (the same pattern `shifts.html`/`plugin_settings.html` already use), the
   dropdown hide is gated on `event.detail.successful`, and a dedicated
   `#buttons-add-error` element plus a page-level `htmx:responseError`
   listener render the server's fragment.

Tests added by Dev: SKU projection (`internal/data`), SKU fallback +
barcode-still-wins (`internal/ui`), non-empty HTML error body
(`internal/ui`), and an end-to-end search→add→tile-appears assertion
(`internal/pages`).

## Independent review

Performed by a separate reviewer session at a different model tier from the
implementer (`complexity:medium` → Opus), in its own isolated worktree, from
the diff alone. **Verdict: safe to merge — three findings, all fixed on this
branch.**

### Blocking finding 1 — CI would have failed: stale manual screenshots

`bash scripts/ci/guard-docs-shots.sh` **failed** on the pushed commit:
`web/ui/partials/buttons_admin.html` is part of the guard's hashed app
surface, so the manifest's `surface_sha256` no longer matched. This is a
CI-blocking guard in `ci.yml`'s `build` job and was missed by Dev/Tester.
Fixed: ran `bash e2e/scripts/docs-shots.sh` (92 Playwright captures, all
green) and committed the regenerated `web/help/img/manifest.json`. No
screenshot content actually changed for this feature — the Designer's new
error element renders empty — the one PNG byte-diff in the commit
(`web/help/img/fa/translations.png`, 10 bytes) is pre-existing capture
nondeterminism on the translations screen, not a UI change. See "Deferred".

### Blocking finding 2 — a false-pass assertion

The new assertion in `internal/pages/buttons_api_test.go` was
`if strings.TrimSpace(rec.Body.String()) == ""` — "the 400 body must not be
blank". **Verified empirically that this passes against the very regression
it claims to pin**: with `ButtonsHTTP.Add` reverted to
`http.Error(w, err.Error(), 400)`, `internal/pages` stayed green (that path
also writes a non-empty body — the raw Go error). Fixed: the assertion now
requires the localized `designer.error.server` copy and explicitly rejects
the raw `"label, code, and itemId are required"` text, matching this file's
existing `TestButtonsStoreErrorsSurfaceAs500` pattern. Re-verified red under
the same revert. As a side effect this now mechanically pins
`ui.designerErrorServerKey` to `pages.buttonsErrorKey` — see the import-cycle
note below — so the duplicated literal can no longer drift silently.

### Blocking finding 3 — the client half had zero test coverage, and one
### failure path was still silent

Nothing in the diff tested the template, yet the client wiring is where the
reported symptom actually lived. Two gaps, both fixed:

- **No regression guard on the `hx-on` gate.** Added
  `TestButtonsSearchResultHidesDropdownOnlyOnSuccess`
  (`internal/pages/buttons_search_vals_test.go`): the rendered
  search-result's `hx-on` must gate on `event.detail.successful` *before*
  hiding the dropdown. Verified red against the pre-fix template
  (`hx-on does not gate on event.detail.successful`).
- **Transport failures were still silent.** Read against the vendored htmx
  1.9.12 source: `xhr.onerror` fires `htmx:afterRequest` (with no
  `successful` property — undefined, so the hide correctly does not run)
  followed by **`htmx:sendError`, not `htmx:responseError`**, and carries no
  response body. So a back-office tablet dropping off the shop LAN mid-tap
  left the dropdown open with nothing explaining why — AC 2's silent-failure
  class, just on a different path. Fixed in
  `web/ui/partials/buttons_admin.html`: both listeners now live in one IIFE
  sharing an element filter, with an `htmx:sendError` arm that shows the
  already-existing `designer.error.server` copy via the mandated page-local
  `var T = { … }` lookup (no new locale key; `guard-i18n.sh` green). Added
  `TestDesigner_RendersAddErrorSurface` (`internal/pages/designer_page_test.go`)
  covering `#buttons-add-error`, both listener registrations, and the
  rendered locale copy; verified red against the pre-fix template.

### Checked and found sound (no change needed)

- **The SQL fallback is correct end-to-end, not just at the JSON layer.**
  Traced `PriceResolverAdapter.Resolve` → `ResolveShortcutLineDecoded`: a
  button whose `code` is a SKU resolves at the *shortcut* step (the
  `shortcut_buttons` row is keyed by that string and carries `item_id`),
  with an exact-SKU lookup behind it as a second net. Matches the issue's
  own live verification that `code=30005` prices Cappuccino correctly.
- **The import-cycle workaround is real, not a shortcut.** Verified by
  actually adding `internal/pages/common` to `internal/ui/buttons.go`'s
  imports and building: `import cycle not allowed`
  (`internal/pages/common/deps.go` imports `internal/ui` for
  `Deps.BtnStore`). Lifting the error handling up to
  `internal/pages/buttons_api.go` instead is not available either —
  `ButtonsHTTP.Add` parses the form and calls the store itself and returns
  nothing to the caller. Duplicating the key with a cross-referencing
  comment is the right call here; it is now also test-pinned (finding 2).
- **The `hx-on` value is valid and parses.** htmx 1.9.12's legacy `hx-on`
  splits on `/^\s*([a-zA-Z:\-\.]+:)(.*)/`, which backtracks correctly over
  the colon inside `htmx:afterRequest`, and compiles the remainder as
  `new Function("event", code)` — so `event` is in scope. Confirmed both by
  reading the vendored minified source and by running the regex and
  `new Function` over the exact attribute value in node: event name
  `htmx:afterRequest`, handler parses. Single line, balanced braces, single
  quotes inside a double-quoted attribute — no escaping hazard, and
  `html/template` treats `hx-on` as a plain (non-`on*`) attribute.
- **`evt.detail.elt` is populated on `htmx:responseError`.** htmx's
  `triggerEvent` sets `detail["elt"] = elt` unconditionally, and
  `responseError` is fired on the originating element — so the listener's
  `classList.contains('result')` filter really does match the search-result
  button. Worth confirming rather than assuming: had this been wrong, the
  entire visible-error half of the fix would have been dead with no Go test
  to catch it.
- **i18n:** `designer.error.server` verified present in **all four** locale
  files (`en`, `fa`, `ar`, `tr`) — checked directly, not taken from the Dev
  report. Pre-existing key (added by ut-docs#944), so no
  `ut-plugin-language-{de,es}` follow-up and no `lang-pack-drift` exposure:
  `web/locales/en.json` is untouched by this branch.
- **Repository pattern:** the only SQL touched is
  `SearchItemsForShortcuts` in `internal/data/pos_repo.go`.
  `guard-data-access.sh` green.
- **RTL / design tokens:** no new colors, spacing or physical `left`/`right`
  properties; the new element reuses the existing `muted` class and
  `aria-live="polite"`, identical to `shifts.html`'s `#shift-result`.
- Checked this pipeline's two recurring bug classes (a file-write handler
  missing `os.MkdirAll`; a cwd-relative path where `paths.Data(...)`
  belongs) — neither applicable, no file I/O in this diff.
- No real client/shop name and no secret-shaped literal anywhere in the
  diff; test fixtures are `Loose Screw` / `Hide Probe` / `SKU-ONLY-1`.

## TDD claims re-verified personally

Every claim was reproduced red-then-green by the reviewer by reverting only
the production code (never the test) in an isolated worktree, atomically
within a single turn:

| Reverted | Test | Red result |
|---|---|---|
| `AddVals()` SKU fallback | `TestSearchResultAddVals_FallsBackToSKUWhenBarcodeEmpty` | `code = "", want SKU fallback "SKU-ONLY-1"` |
| `AddVals()` SKU fallback | `TestButtonsSearchHxValsFallsBackToSKUForBarcodeLessItem` | `code = "", want SKU fallback "SKU-ONLY-1"` |
| `COALESCE(i.sku,'')` projection | `TestSearchItemsForShortcuts_SKUOnlyItemCarriesSKU` | `SKU = "", want "SKU-ONLY-1"` |
| `COALESCE(i.sku,'')` projection | `TestSearchItems_MatchesNameSkuBarcodeWithPaging` | `SKU = "", want "ORG-01"` |
| localized 400 body | `TestButtonsHTTPAdd_StoreValidationErrorRendersNonEmptyHTMLBody` | `raw Go validation error text leaked…` |
| localized 400 body | `TestButtonsAddValidatesPersistsAndNormalizesImage` | **passed pre-fix** → false-pass, fixed (finding 2), now red |
| pre-fix `buttons_admin.html` | `TestButtonsSearchResultHidesDropdownOnlyOnSuccess` (new) | `hx-on does not gate on event.detail.successful` |
| pre-fix `buttons_admin.html` | `TestDesigner_RendersAddErrorSurface` (new) | `no #buttons-add-error element` |

`TestSearchResultAddVals_PrefersBarcodeOverSKU` correctly stayed green under
the revert — it guards the unchanged common case, as intended.

## Gate run (reviewer's own worktree, after the fixes)

- `gofmt -l .` — no output.
- `go build ./...`, `go vet ./...` — clean.
- `go test ./internal/data/... ./internal/ui/... ./internal/pages/...` —
  green (`data` 114s, `ui` 0.2s, `pages` 139s, `pages/catalog`,
  `pages/common`).
- `go test ./...` (full repo) — green, no failures anywhere.
- CI `build`-job guards, all 26 run individually: `guard-data-access`,
  `guard-data-access_test`, `guard-migration-version-collision(+_test)`,
  `guard-kiosk-engine(+_test)`, `guard-plugin-menu-read(+_test)`,
  `guard-i18n(+_test, +_toast_test)`, `guard-compliance-claims(+_test)`,
  `guard-docs-shots(+_test, +-cross-check_test)`, `guard-help-topics`,
  `guard-webkit-version`, `guard-kiosk-launch-flags`,
  `guard-android-status-address`, `guard-android-i18n`, `guard-emoji-font`,
  `guard-htmx-loaded`, `guard-autofill-suppression(+_test)`,
  `guard-osk-loaded(+_test)`, `check-brand-assets`,
  `guard-makefile-version` — **all PASS** (`guard-docs-shots` only after the
  regeneration in finding 1).

## Acceptance criteria

1. *Barcode-less item addable from Designer search* — met for a SKU-only
   item, covered by an end-to-end search→add→tile test. See "Deferred" for
   the residual identifier-less case.
2. *Failed add visible, never a silent dropdown close* — met for 400, 500
   and, after this review's fix, transport failure; the dropdown now stays
   open on every failure path.
3. *Regression tests* — met, at API and template level, all independently
   verified red pre-fix.

## Deferred (noted, not fixed here)

- **An item with neither a barcode nor a SKU still cannot be added.**
  `items.sku` is nullable and ut-docs#1176 deliberately stores `NULL` for an
  item created/imported without a source SKU, so such an item is findable by
  name in Designer search but posts `code=""` and now gets a visible-but-
  generic "Something went wrong". AC 1's parenthetical ("the operator must
  not care which identifier the item happens to have") arguably reaches
  this, but closing it means minting a code server-side from the item ID —
  i.e. putting a UUID into `shortcut_buttons.barcode` weeks after #1176
  deliberately took UUIDs *out* of `items.sku`. That is a design decision,
  not a bug fix; it belongs on its own card with the Architect, not in this
  branch. The reported bug and its named repro (SKU `30005`) are fully
  fixed.
- **`.error` has no standalone CSS rule.** `web/public/app.css` styles only
  `.pos-notice.error`, `.toast.error` and `.split-tender-status.error`, so
  the server's `<div class="error">` fragment inherits the `muted` grey of
  its container rather than `--danger`. This diff is faithful to the
  existing precedent (`shifts.html`'s `#shift-result` is `class="muted"`
  too), so fixing it here would diverge one screen from the rest; it wants a
  product-wide card (give the fragment a real error style, or a dedicated
  class) covering Designer, Shifts and Refund together.
- **A failed *remove* is still silent.** The `htmx:responseError` listener
  filters on `.result`, so the tile's remove form — which still answers with
  a raw `http.Error` — has no visible failure path. Pre-existing, untouched
  by this card.
- **`ButtonsHTTP.Add`'s `r.ParseForm()` error branch** still uses raw
  `http.Error`. Practically unreachable for a form POST; left alone rather
  than widening the diff.
- **`docs-shots` capture nondeterminism.** Three separate regeneration runs
  produced byte-differing PNGs for the translations/users screens each time
  (a different file on each run), suggesting a timing-dependent element on
  the manager-gated translations page. Harmless — the guard hashes the app
  surface, not the PNGs — but it means every regeneration commits a little
  unrelated image churn. Worth a card if it keeps showing up.
- **The manual needs no change.** `web/help/en/designer.md` describes
  arranging quick buttons in general terms and never claimed an item needed
  a barcode, so no prose has gone false; the Designer screen itself is
  visually unchanged (the new error element renders empty).

## Verdict

**Safe to merge.** Three blocking findings — a CI guard that would have
failed (`guard-docs-shots`), a false-pass assertion, and an untested client
half with one still-silent failure path — all found by this review and fixed
on the same branch, with every fix re-verified red-then-green. Full gate
green afterwards.
