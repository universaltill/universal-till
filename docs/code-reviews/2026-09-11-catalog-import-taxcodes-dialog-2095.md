# 2026-09-11 — Catalog: Import/Tax codes open as a closable dialog (ut-docs#2095)

## What shipped

`/items` (ut-docs#1950) is a master-detail shell: left rail + `#items-panel`.
Modifiers/Option-sets (ut-docs#2090) stay inside the shell by swapping
`#items-panel`. Import and Tax codes are **not** `/items` rail sections, so
this card instead makes them open as a **closable full-screen `<dialog>`
overlay** above the shell:

- `internal/pages/import_page.go` / `internal/pages/tax_codes_page.go`:
  `GET /import` / `GET /catalog/tax-codes` gain a fragment-render branch
  keyed on `httpx.IsFragmentSwap(w, r)` — a fragment request renders just
  the `content` template block (deliberately **no** rail OOB swap, since a
  dialog overlay never replaces `#items-panel`); a normal request is the
  unchanged full standalone page.
- `web/ui/pages/catalog.html`: the Import/Tax codes buttons, when
  `.InItemsShell`, become `hx-get` → a `<dialog>` (`#import-modal`/
  `#tax-codes-modal`) opened via `.show()`. Outside the shell, unchanged
  plain link.
- `web/ui/pages/import.html` / `web/ui/pages/tax_codes.html`: the existing
  back-link gains a third mode — inside the dialog, it closes the dialog
  instead of navigating.
- New `e2e/tests/items-shell-catalog-import-taxcodes-dialog-2095.spec.ts`
  (6 specs) and two new Go tests pinning the fragment-vs-full-page split.
- Landed on top of a same-day merge conflict with ut-docs#2092 (icon-only
  top action row, Modifiers/Option-sets removed from the row entirely) —
  resolved by combining #2092's icon-only markup with this card's dialog
  behaviour, per that PR's own comment anticipating exactly this merge.

## Independent review (Opus, isolated worktree, different model from the Sonnet implementation)

Full transcript findings below; this record covers the *outcome*, not a
re-statement of the raw report.

**Verdict on first pass: NOT safe to merge — 3 blockers, all fixed before merge:**

1. **`showModal()` made the on-screen keyboard unusable on kiosk hardware.**
   Both new dialogs contain real text inputs (tax-code name/rate fields,
   the import file picker). `showModal()`'s top-layer/inert-outside
   behaviour makes `#osk` (appended to `<body>`, never re-parented into an
   open dialog) unreachable — the same class of bug as ut-docs#1385,
   already fixed five times elsewhere in this codebase
   (`#hold-modal`/`#pfand-modal`/`#elevation-modal`/`#table-add-modal`/
   `.payment-overlay`/`#item-form-modal`). Measured: an OSK key click
   timed out (`locator.click: Timeout 3000ms exceeded`) with the dialog
   open. **Fix**: switched both dialogs from the showModal()-oriented
   `.modifier-modal` class to the existing `.item-form-modal` class
   (`.show()`, full-bleed, `position: fixed`, correct `z-index`, and it
   already carries the `body.osk-padded .item-form-modal` keyboard-
   clearance rule for free — no new CSS needed for that part).
2. **`hx-on::after-request` opened the dialog on failure too, with no way
   out.** Unguarded, a non-2xx response (e.g. a cashier without the
   `tax_code_management`/`import_export` permission) still called
   `.show()`, trapping the operator in an empty/erroring modal with no
   close control reachable on a touch-only till — a hard modal blocker,
   which `CLAUDE.md` §10 (ut-docs#1999) forbids outright on an admin
   surface. **Fix**: guarded both `hx-on::after-request` handlers on
   `event.detail.successful`, the same idiom `catalog_variants.html`/
   `self_order_cart.html` already use.
3. **The `/import` permission-denial redirect nested the whole Catalog page
   inside the dialog.** `GET /import`'s permission check answered with
   `http.Redirect(w, r, "/catalog", ...)` unconditionally; htmx follows a
   same-origin redirect transparently *and* preserves the `HX-Request`
   header across the hop, so a denied cashier's tap resolved to `GET
   /catalog`'s own fragment response (content + rail OOB swap) landing
   inside `#import-modal` — measured 86KB swapped in, a second nested
   `#import-modal`, duplicate ids throughout. **Fix**: branch on
   `httpx.IsFragmentSwap` in the permission check too — a fragment request
   now gets a real `403` via `httpx.RenderError` (the same helper
   `GET /catalog/tax-codes`'s own permission check already uses for the
   identical page-route reason `guard-page-http-error.sh` enforces), caught
   by the button's own `successful` guard from finding 2. The standalone
   (non-fragment) redirect is unchanged.

All three fixes got their own regression coverage: a Go test pinning the
403-not-redirect fragment response, and two new e2e specs (OSK
typability — drives a real click + key press through `#osk` and asserts
the input's value actually changed; and a forced-500 route intercept
asserting the dialog stays closed). Both e2e specs were confirmed to fail
against the pre-fix code (stashed the two fixed files, kept the tests) —
genuine red before green, not just asserted.

**Should-fix / accepted, not blocking:**

4. Three links reachable *inside* the open dialogs (`import.html`'s
   post-import "View catalog", the tax-codes `?` help link, "Manage
   per-plugin overrides →") still plain-navigate out of the shell to a
   railless standalone page. The most relevant one (View catalog, emitted
   from Go in `import_page.go`) needs the same `IsFragmentSwap` plumbing
   this card added to the page handler, threaded into `POST /api/import`'s
   own success response — a real but separable follow-up. Filed:
   **ut-docs#2112**.
5. A CSS comment claiming the standalone `/catalog/tax-codes` page was
   "unaffected either way" by the new dialog-scoped table-scroll rule was
   measured false (the standalone page overflows *worse* at 360px,
   pre-existing, not caused by this diff) — corrected in-branch; the
   underlying standalone-page clipping itself filed separately: **ut-docs#2113**.
6. Tax-codes table at 360px hides Edit/Deactivate off-screen with only an
   undiscoverable swipe to reach them — matches the existing
   `.barcode-backfill-modal` precedent's own accepted trade-off; noted, not
   fixed here.
7. Pre-existing, out of scope: `/items?lang=fa` first load renders the rail
   in Persian but the panel's own buttons in English (`items_page.go`'s
   `embedItemsSection` sub-request has no `?lang=` and no cookie yet on a
   first visit). Predates this diff (#1950). Filed: **ut-docs#2114**.

## Verified beyond automated tests

- Full gate, both before and after the merge-conflict resolution with
  ut-docs#2092: `gofmt -l .` (empty), `go build ./...`, `go vet ./...`,
  full `go test ./...` (0 failures), `golangci-lint run ./...` (0 issues),
  `guard-i18n.sh`, `guard-data-access.sh`, `guard-page-http-error.sh`,
  `guard-help-topics.sh`, `guard-help-drift.sh` (only pre-existing
  baselined entries), `guard-docs-shots.sh`, `guard-compliance-claims.sh` —
  all green.
- e2e: the new spec (6/6), the #2090 predecessor spec (3/3, itself updated
  by #2092's own merge to trigger via the rail rather than the removed
  top-row buttons), and #2092's own spec (4/4) — 13/13 together, run for
  real against a live server, after the merge.
- TDD genuinely re-verified, twice: the original fragment/Vary-header split
  (revert handlers only, keep tests → real assertion failure, not a
  compile error; restore → green) and the three blocker fixes (revert
  `catalog.html`/`app.css` only, keep the new e2e specs → both new specs
  fail for the expected reason; restore → green).
- Visual: real screenshots taken and looked at (not just asserted) at
  1024×600 and 360×800, `en` and `fa` (RTL), both dialogs, before and after
  the blocker fixes and again after the #2092 merge — rail/backdrop intact
  behind the (now full-bleed) dialogs, no clipping/overlap, RTL renders
  correctly, tax-codes table scrolls instead of silently clipping at
  1024×600.
- Not verified: a physical kiosk Pi or pilot tablet (no hardware in this
  cloud session) — the OSK-reachability fix was verified via the *actual*
  on-screen keyboard widget (`#osk`) driven through a real click + key
  press in Playwright, which is the same mechanism kiosk hardware uses,
  but not on the real hardware itself.

## Safe-to-merge verdict

Safe to merge. All three blocker-class findings are fixed with their own
regression coverage; the full gate and both predecessor/sibling e2e suites
are green after resolving the same-day merge conflict with ut-docs#2092.
