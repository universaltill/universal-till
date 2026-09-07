# Refund total preview (ut-docs#1217)

## What shipped

The refund screen (`/refund/{receipt}`) had no total preview before
submit — a cashier picked lines/quantities and hit Refund with no visible
figure until the completed return's total appeared on the post-refund
journal page. This adds a live-updating total that tracks quantity edits
before submit, computed server-side from the exact same math the real
refund uses (never reimplemented client-side).

- **`internal/pages/refund_page.go`**: extracted the POST handler's inline
  double-refund-guard-state loads (`loadRefundGuardState`) and its
  quantity/discount/service-charge computation
  (`refundLinesFromForm`) into two reusable functions, unchanged in
  substance from the original inline code. Added `POST /api/refund/preview`
  — read-only, no manager-PIN gate (no persisted mutation, no audit row),
  reusing those two functions plus the pre-existing `computeRefundTotal`.
- **`web/ui/pages/refund.html`**: each `qty_N` input now also posts to the
  preview endpoint (debounced 200ms) targeting a new `#refund-total`
  element; the element self-triggers once on page load so the default
  (full-remaining-quantity) total shows immediately.
- **`web/locales/{en,ar,fa,tr}.json`**: new `refund.total` key. The
  external `ut-plugin-language-{de,es}` packs got the same key first
  (universaltill/ut-plugin-language-de#176,
  universaltill/ut-plugin-language-es#175, merged before this PR per the
  standing "pack lands before core merges" rule — `lang-pack-drift` is
  blocking on push to `main`).
- **`web/help/{en,ar,fa,tr}/sell.md`**: step 6 (refund) gets a sentence
  about the live total; `make docs-shots` regenerated (screenshots + manifest).

## Independent review (Opus, isolated worktree, ut-docs#1217)

**One blocking finding, fixed and re-verified:**

- **B1 — the extraction silently dropped the `len(lines) == 0` → 400
  guard from the real `POST /api/refund` path.** `refundLinesFromForm`
  returns `nil, 0, 0, nil` for an empty selection (correct — the preview
  path needs that to render a real zero, not an error), but my first cut
  of the POST handler only checked `err != nil` after the call, not
  `len(lines) == 0` — I had written that check once, then deleted it by
  accident while removing an unrelated orphaned duplicate block from the
  same edit pass. An empty submit then fell all the way through to
  `pos.CompleteSale`, which happens to also answer 400 (for a different
  reason), which is exactly why the pre-existing
  `TestPostRefund_NoQuantitiesSelected` (status-only) didn't catch it. On
  the way there it called `EnsurePaymentMethod` (a real DB write for an
  otherwise-rejected request), fired `dispatchFiscalSignStart` (opening a
  TSE-side transaction for a refund a plain input mistake was always
  going to refuse — the exact defect class ut-docs#1519 already fixed two
  comments above that call site), and could reach a payment plugin's
  refund webhook with amount 0.

  **Fixed**: restored `if len(lines) == 0 { http.Error(w, "select at
  least one item to refund", http.StatusBadRequest); return }`
  immediately after the `refundLinesFromForm` call in the real POST
  handler. **`TestPostRefund_NoQuantitiesSelected` strengthened** to
  assert the literal rejection body (not just the 400 status) and that no
  `return` sale row exists afterward — re-verified by reverting the fix,
  confirming the test fails with exactly the wrong (generic
  tender-failure) body, then restoring and confirming it passes again.

**Non-blocking findings, addressed:**

- **N1 — the manager PIN was included on every keystroke.** The original
  wiring listened for `input`/`change` bubbling from anywhere in the form
  (`hx-trigger="input from:closest form …"`), so typing into the
  password-type `manager_pin` field also fired a preview request carrying
  the in-progress PIN, to a handler that ignores it. Fixed: the live
  triggers now live directly on each `qty_N` input (`hx-include` scoped
  to `[name='receipt'], [name^='qty_']`), so editing the PIN field never
  fires a preview request or includes the PIN at all. The total's own
  `load` trigger still shows a real total immediately on page open.
- **N2 — an unknown/error preview state rendered as a confident `£0.00`,
  indistinguishable from a genuine "nothing selected" zero.** Fixed: an
  unrecognized receipt, a guard-state load failure, or a malformed/
  over-limit in-flight edit now render `—` (the repo's existing bare-
  punctuation placeholder convention, see `settings_page.go`'s own
  label); a deliberate empty selection still renders a real `£0.00`, since
  that is the true answer in that case. New test
  `TestRefundPreview_UnknownReceiptShowsUnknownPlaceholderNotZero` pins
  the distinction.
- **N3 — a dropped connection on the background preview request painted
  the same "something went wrong" message under the Refund button a real
  failed submit does**, via the page's shared `htmx:sendError`/
  `htmx:responseError` handlers (ut-docs#1287). Fixed: both handlers now
  check `ev.detail.requestConfig.path === '/api/refund/preview'` (verified
  against the actual vendored `htmx.min.js` source — `requestConfig.path`
  is populated on both event types) and return early for it. Live-verified
  with `page.route(...).abort()`: a broken preview request now leaves
  `#refund-msg` empty, while a broken real submit still shows the error
  message exactly as before (ut-docs#1287 regression re-checked, not just
  assumed unaffected).
- **N6 — a Turkish grammar slip** ("gönderemeden" — impotential form,
  "without being able to send" — instead of "göndermeden", "before
  sending") and an ambiguous Arabic verb form (`يتحدث`, reads as "speaks"
  rather than "updates") in the new help sentence. Fixed both;
  `make docs-shots` re-run since the app surface changed again.

**Left as-is, explicitly out of scope for this card:**

- **N4 — `qty_0=NaN` bypasses the quantity-remaining guard** (`qty <= 0`
  and `qty > remaining+1e-9` are both false for `NaN`), reaching
  `pos.SaleLineInput` and every downstream computation before
  `pos.CompleteSale` finally rejects it (400, nothing persisted —
  confirmed). **Pre-existing on `main`, carried verbatim by the
  extraction, not introduced here.** Filed as ut-docs#1711 for a
  dedicated `math.IsNaN` rejection.
- **N5 — unrelated screenshot byte-churn** (`till-designer.png`,
  `multitill.png`, `sell.png` shift by a handful of bytes across two
  `make docs-shots` runs). Expected non-deterministic rendering noise
  from the full-suite regeneration this card's docs change required, not
  a defect.
- **N7 — cost per keystroke.** Each debounced preview issues 6 SQLite
  round trips (`GetSaleDetail` + 5 guard-state reads) and re-derives the
  full per-line discount clamp. Fine for a typical basket on local
  SQLite; noted for future reference, not actionable now.

## Verified beyond automated tests

- **TDD**: all 5 new preview tests + the strengthened
  `TestPostRefund_NoQuantitiesSelected` were confirmed to fail against
  the pre-fix code with the exact wrong output, then pass after.
  Independently re-verified again by the Opus reviewer in an isolated
  worktree (two separate breaks: a wrong `computeRefundTotal` input, and
  a disabled route — both produced the expected failures).
- **Real driven browser runs** (Playwright against the pre-installed
  Chromium, a real `go run .` server, seeded completed sales): total
  renders correctly on load and updates live on quantity change, in
  English and Persian/RTL (screenshots reviewed — correct alignment,
  logical-CSS layout, no overlap in either direction); a preview network
  failure stays silent while a real submit's network failure still shows
  the error message (N3's fix, live-verified both ways); dark-theme spot
  check (this app's theme is a server-side setting, not
  `prefers-color-scheme` — the new element reuses only pre-existing
  shared classes, so risk here was already low).
- Every CI-blocking guard in `ci.yml`'s `build` job run locally and green:
  `guard-data-access`, `guard-kiosk-engine`, `guard-plugin-menu-read`,
  `guard-page-http-error`, `guard-i18n`, `guard-compliance-claims`,
  `guard-docs-shots`, `guard-help-topics`, `guard-htmx-loaded`,
  `guard-autofill-suppression`, `guard-e2e-fixtures-import`.
  `go build ./...`, full `go test ./...` (whole repo), `gofmt -l .`
  (clean), `golangci-lint run ./...` (0 issues).

## Verdict

**Safe to merge** after the B1 fix. No compliance/money-correctness gap
remains open; N1/N2/N3/N6 addressed; N4/N5/N7 explicitly deferred with
reasoning above (N4 tracked as ut-docs#1711).
