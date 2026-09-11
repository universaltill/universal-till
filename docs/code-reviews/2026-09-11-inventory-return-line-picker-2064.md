# Code review: inventory "Process a return" line-picker fix (ut-docs#2064)

**Date**: 2026-09-11
**Author**: Farshid Mirza (autonomous SDLC pipeline, `lane:cloud-54`) — Dev
phase run inline (Sonnet, `complexity:medium`), orchestrated and committed
by this session.
**Reviewer**: independent fresh-context Opus subagent, isolated git
worktree, read-only pass (no prior context on the change)
**Ticket**: universaltill/ut-docs#2064
**Branch**: `fix/2064-inventory-return-line-picker`

## What shipped

The Inventory page's **Process a return** panel (`web/ui/pages/inventory.html`)
only ever submitted `receipt_no` + `reason` — the real handler,
`CreateReturn` (`internal/pages/inventory_api.go`), always refused with
"at least one line required" because there was never a way to pick which
lines to return. Confirmed via `git log`/code reading that this was a
real, pre-existing gap (not a regression) — the form-encoded branch even
carried its own comment admitting it: "Note: Form handling for lines
array would need custom parsing."

Fix:

1. **New `GET /api/inventory/return/lines?receipt_no=...`** (`GetReturnLines`)
   renders an htmx-swapped line-picker: item name/SKU, sold quantity, and
   a `qty_N` input per line (N = positional index into the sale's own
   line order — same convention `refund_page.go`/`refund.html` already
   use for their sibling flow).
2. **`CreateReturn`'s form-encoded path now actually parses `lines[]`**
   from those `qty_N` fields, backfilled once the original sale + its
   line snapshots are fetched (moved earlier in the function so the
   backfill has them available). The JSON API path is unchanged — the
   existing `TestCreateReturn_ValidationErrors` (asserting `lines:[]`
   still 400s for a JSON request) stays green throughout.
3. `web/ui/pages/inventory.html`: the receipt field now `hx-get`s the
   picker on `change`; the panel's stale copy ("Returns the entire sale
   (line selection coming later)") is replaced with accurate wording.
4. i18n: reused `journal.detail.item`/`refund.col.sold`/
   `refund.col.remaining` where the semantics genuinely matched; added
   two new, purpose-built keys (`inventory.return_col_qty`,
   `inventory.return_no_lines`) where reusing an existing key would have
   been misleading (see review findings below) — all four shipped
   locales (en/tr/fa/ar) translated directly, no baseline entries needed
   for the keys themselves.
5. `web/help/en/inventory.md` gained a **Processing a return from here**
   section (English-only, `help-drift-baseline.json` entries added for
   ar/de/fa/tr citing this card, same convention as ut-docs#329/#332);
   `make docs-shots` regenerated the manifest (the `/inventory` screen's
   own screenshot pixels are unchanged — the new picker only appears
   once the already-collapsed `<details>` panel is expanded and a
   receipt is entered, so the default page view is identical).

## Independent review findings and disposition

The Opus review ran the full gate itself (build/vet/lint/tests/guards),
independently re-verified the TDD claim (reverted the fix in an isolated
worktree, confirmed the new tests fail/fail-to-compile with the actual
error, restored and confirmed green), and found the diff **not** safe to
merge as submitted. All required fixes were applied on this branch
before merge:

1. **[blocker, fixed] `guard-docs-shots.sh` was red.** The screen's own
   surface hash had moved (new Go handler + HTML changes) but
   `make docs-shots` hadn't been run. Fixed: ran it, kept only the
   `inventory` topic's real (markdown-hash) update plus the regenerated
   manifest; reverted four unrelated PNGs that regenerated with pure
   anti-aliasing noise (`ar/till-designer.png`, `en/catalog.png`,
   `en/sell.png`, `tr/catalog.png` — confirmed via `git diff` on
   `manifest.json` that none of those topics' own content hashes moved,
   same noise class PR #1068's own review already documented).

2. **[should-fix → promoted and fixed as a correctness fix, not deferred]
   Unbounded double-return.** `CreateReturn` validated a requested
   quantity only against the ORIGINAL line's sold quantity, never
   against what a prior return/refund already took back — and never set
   `pos.SaleLineInput.RefundOfLineID` on the lines it created, so a
   return made through this endpoint was invisible to
   `repo.ReturnedQuantitiesByOriginalLine` (the exact map
   `refund_page.go`'s own over-refund guard reads). The reviewer verified
   this was live-reachable (two submissions of the same full quantity
   both succeeded) and noted my own code comment claiming this was
   "tracked separately" cited no real issue number.
   This was reachable in practice *before* this review too — confirmed
   independently: driving ut-docs#2064's own fix through a real running
   till returned the same receipt three times in a row, each one
   succeeding, before this finding was ever raised. Fixed on this branch:
   `returnLine.RefundOfLineID = reqLine.LineID` set on every persisted
   return line; both `CreateReturn`'s own validation and
   `GetReturnLines`'s picker now net against
   `repo.ReturnedQuantitiesByOriginalLine(ctx, originalSaleID)` (same
   repo method `refund_page.go` already uses for its own cap), so a line
   already fully returned — through THIS endpoint or through `/refund` —
   reads and enforces `Remaining = 0` on both sides. Filed as
   universaltill/ut-docs#2069 for the paper trail (found reviewing
   ut-docs#2064, before it shipped — never actually merged in the broken
   state) and cited by name in the code's own comments.
   Regression coverage added: `TestCreateReturn_SetsRefundOfLineID`,
   `TestCreateReturn_SecondReturnAgainstSameLineIsCapped`,
   `TestGetReturnLines_RemainingNetsPriorReturn`. TDD-verified: reverting
   just the `RefundOfLineID`/netting change (`git stash`) fails to
   *compile* (`ReturnLineView has no field Remaining`), confirming the
   new tests genuinely depend on the fix.

3. **[should-fix, fixed] Wrong empty-state copy.** `common.no_results`
   ("No matches — try a different search.") was reused for three
   different real situations (unknown receipt, an ineligible sale, a
   sale with no lines) and reads like a search hint to someone who typed
   a receipt number, not searched. Replaced with a dedicated
   `inventory.return_no_lines` key, translated in all four locales.

4. **[should-fix, fixed] Form-path errors were silently swallowed.** The
   vendored htmx (1.9.12, no `responseHandling` config) never swaps a
   non-2xx response, and `inventory.html` had no `htmx:responseError`
   listener — so every real rejection (400 "at least one line required",
   the new 400 over-quantity message, a 409 from the fiscal gate) landed
   nowhere visible, which is close to the exact "I press the button and
   nothing happens" shape this card was filed about. Added a listener
   scoped to this form's own request path (same `requestConfig.path`
   check `refund.html`'s own `isRefundPreviewRequest` uses, so the
   page's other forms are untouched) that writes the response body into
   `#return-result`. Verified live: forcing a stale over-quantity
   submission now visibly shows "invalid return quantity 2.00 for line
   … (max 0.00 remaining)" under the Process Return button, where before
   this fix nothing appeared at all.

5. **[should-fix, fixed] No manual update.** Added the "Processing a
   return from here" section described above.

6. **[nit, fixed] Inert `min`/`max` on `type="text"`.** Switched the
   qty input to `type="number"` (matching `refund.html:43`'s own
   convention for the identical job), so the browser-side cap is real,
   not just the `pattern` attribute.

7. **[nit, fixed] No NaN/Inf guard.** Mirrored `refund_page.go`'s
   ut-docs#1711 fix: `math.IsNaN`/`math.IsInf` now rejected explicitly
   with a clear message, instead of falling through to a confusing
   "return total must be positive."

8. **[nit, fixed] "Refund qty" header inside a non-monetary return
   panel.** Replaced with the new `inventory.return_col_qty` key ("Qty
   to return").

9. **[nit, accepted, not changed] JSON error-path reordering.** Moving
   the original-sale-detail fetch earlier means
   `{"original_sale_id":"no-such-sale-id","lines":[]}` now 404s
   ("original sale not found") where it previously 400'd ("at least one
   line required"). No existing test covers that exact combination; the
   reviewer called the new ordering "arguably more correct" (a
   nonexistent sale is a more specific problem than an empty selection).
   Left as-is.

10. **[nit, accepted, noted rather than chased] `de`/`es` language packs
    will keep shipping the OLD `inventory.return_note` translation.**
    `lang-pack-drift` checks key *parity*, not value freshness, so a
    changed VALUE on an EXISTING key is invisible to any CI this
    pipeline can see. No established mechanism flags this class of drift
    today (unlike a brand-new key, which the advisory `::warning::` does
    catch). Noted here rather than filing a cross-repo follow-up for a
    single cosmetic sentence; worth a real fix if this class of gap
    recurs.

## Verified beyond automated tests

- **Real running till, three separate sessions**, driven with a
  Playwright script against `e2e/run-till.sh` (throwaway SQLite DB,
  demo catalogue), not just `httptest`:
  - Desktop (1280×900), kiosk floor (1024×600), and phone (360×800):
    picker renders, submits, and the success message shows at all three;
    screenshots taken and read at each breakpoint — no overlap, clipping,
    or broken wrapping at any size.
  - RTL (`?lang=ar`): full mirrored layout, translated column headers and
    note text, `document.dir === 'rtl'` — read and confirmed correct.
  - Dark/alternate theme (`POST /api/settings/theme theme=dark`):
    rendered correctly, no unstyled or broken element.
  - The app's own on-screen-keyboard guard (`osk.js`) applied
    `inputmode="none"` to the new qty input automatically — confirmed
    live in the rendered DOM, no double-keyboard risk introduced.
  - Netting fix, end to end: seeded a real completed sale, returned it
    in full through the real UI (200, stock/cash-back persisted), then
    re-fetched the picker for the same receipt and confirmed
    `Remaining = 0`; forced a stale-client over-quantity resubmit and
    confirmed both the server 400 and the now-visible on-screen error.
- **TDD discipline, both rounds**: every new/changed behavior was
  confirmed to fail (build failure or wrong status/assertion) against
  the pre-fix code before being confirmed green, both by this session
  and independently by the reviewer in its own isolated worktree.
- Every server started for manual verification was killed in the same
  turn it was started in; no real shop/client name used anywhere in
  seed data (demo catalogue items, `R-E2E-*`/`R-RETURN-*` receipt
  numbers only).

## Gate (final, on this branch)

`gofmt -l .` clean · `go build ./...` clean · `go vet ./...` clean ·
`golangci-lint run ./internal/...` 0 issues · `go test ./...` (full
repo) all green · `guard-data-access.sh`, `guard-kiosk-engine.sh`,
`guard-plugin-menu-read.sh`, `guard-page-http-error.sh`, `guard-i18n.sh`,
`guard-compliance-claims.sh`, `guard-docs-shots.sh`,
`guard-help-topics.sh`, `guard-help-drift.sh`, `guard-htmx-loaded.sh`,
`guard-autofill-suppression.sh`, `guard-e2e-fixtures-import.sh` all ✓.

## Explicitly deferred (filed, not fixed here)

- universaltill/ut-docs#2069's own acceptance criteria are satisfied by
  this branch (RefundOfLineID set, netting applied on both read and
  write paths, cross-flow — Inventory return vs. `/refund` — confirmed
  to share the same pool since `ReturnedQuantitiesByOriginalLine` groups
  by original line id regardless of which endpoint created the return
  row) — that card can be closed once this PR merges.
- `de`/`es` external language-pack translation of the changed
  `inventory.return_note` value (see finding 10 above) — no CI catches
  this class of drift; noted, not filed as a separate card given the
  small, cosmetic scope.

## Verdict

**Safe to merge.** The independent review's blocker and all should-fix
findings are resolved on this branch, with regression coverage and live
verification for each; the two accepted nits are documented above with
reasoning, not silently dropped.
