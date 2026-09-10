# Code review: order-status action buttons match current status (ut-docs#1964)

**Date:** 2026-09-10
**Card:** universaltill/ut-docs#1964
**PR:** fix/1964-order-status-buttons
**Complexity:** medium (Dev: Sonnet inline, Review: Opus subagent, fresh
context, isolated worktree — per `MODEL-ROUTING.md`)

## What shipped

The Orders board (`/orders`, `/kitchen-display/{station}` — shares the same
partial — and `/orders/{receipt}`) showed all four status-change buttons
(Preparing/Ready/Collected/Cancel order) on every row unconditionally,
regardless of the order's current status. A tap that applied left its own
now-inapplicable button sitting there until the next 15s poll/SSE push, and
the buttons were full page-action size on a list row (product-owner bug
report, 2026-09-10).

- `internal/pages/order_status.go`: new `orderAction`/`orderActionCandidates`
  (the fixed, ordered candidate list) and `allowedOrderActions(current)`,
  which filters that list through the **existing** `pos.OrderStatusAllowed`
  conflict-rule predicate — the same one the write path already enforces, so
  a row can never offer a button its own tap would silently no-op. Reused,
  not re-derived.
- `writeOrderStatusFragment` (the shared OOB-fragment writer, called from
  both the local-apply and primary-proxied replica write paths) now always
  also emits `writeOrderActionsFragment` (list/kitchen-display board,
  `order-actions-{receiptNo}`) and `writeOrderCollectFragment`
  (`order_view.html`'s own narrower Collect-only scope,
  `order-collect-{receiptNo}`) — two more OOB `<div>`/`<span>` swaps
  alongside the pre-existing status-cell and terminal row-delete ones.
  Each targets a different id and htmx no-ops on whichever id the posting
  page didn't render, so it's safe to always emit both regardless of which
  page's tap this was.
- `orderRow` gained an `Actions []orderAction` field, computed once in
  `orderRowsFor` via the same `allowedOrderActions` call — the full-list
  render and the post-write OOB fragment can never drift on which buttons a
  given status shows.
- `web/ui/partials/orders_list.html`: the actions `<td>` now ranges over
  `.Actions` inside `<div id="order-actions-{{ .ReceiptNo }}" class="btn-actions">`
  instead of hardcoding all four buttons — `.btn-actions` is the
  **existing** compact row-action style already used identically in
  `catalog_row.html`/`tax_codes_table.html`, not a new class. Uses the
  established `{{ jsonVals "status" .TargetStatus }}` helper for `hx-vals`
  instead of a hand-written JSON literal (`guard-i18n.sh` check 4 caught
  this on the first pass; fixed before Tester).
- `web/ui/pages/order_view.html`: the lone Collect button is wrapped in
  `<span id="order-collect-{{ .Sale.ReceiptNo }}">` so it can be OOB-swapped
  away once terminal — deliberately its **own** id, not
  `order-actions-{receipt}` (that page only ever offers one button and must
  never inherit Preparing/Ready/Cancel).
- Kitchen display (`/kitchen-display/{station}`) inherits the fix for free
  — it renders the same `orders_list.html` partial from the same
  `orderRowsFor`.
- `web/help/img/manifest.json` regenerated via `make docs-shots` (full
  112-screenshot run, pre-installed Chromium). Only the surface hash moved;
  no `.png` bytes changed — the `order-status` topic's own docs-shots seed
  never exercises a non-"new" order (its screenshot shows the page's empty
  state), so there is genuinely nothing to re-capture. Verified by diff,
  not assumed.

## Independent review (Opus, fresh context, isolated worktree)

**Verdict: safe to merge, with one test-strength fix applied and
re-verified.**

### Fixed (applied to the branch)

- **`TestOrderViewPage_CollectTap_HidesCollectButtonViaOOB` only asserted
  half the contract.** It checked the server's POST response carries the
  `order-collect-{receipt}` OOB span, but never that `order_view.html`
  itself actually *renders* an element with that id in the first place. An
  OOB swap that targets an id the page never rendered is a silent no-op in
  a real browser — reviewer proved this concretely: deleting the
  `<span id="order-collect-{{ .Sale.ReceiptNo }}">` wrapper from the
  template left the whole `order_view` half of the fix silently dead while
  the **entire Go test suite still passed**. Added a page-GET assertion
  (`GET /orders/{receipt}` must render `order-collect-{receipt}` wrapping
  the Collect button) to the same test; confirmed it now fails on exactly
  that mutation, then confirmed it passes again with the wrapper restored.
  The list side didn't have this gap — `TestOrdersListFragment_ActionsMatchCurrentStatus`
  already reads the id out of the actually-rendered `/ui/orders` fragment.

### Verified clean — no changes needed

- **Lockstep with `pos.OrderStatusAllowed`**, exhaustively checked across
  all six current-status values (`""`, `new`, `preparing`, `ready`,
  `collected`, `cancelled`): `""`/`new` → 4 buttons, `preparing` → 3,
  `ready` → 2, `collected`/`cancelled` → 0. No offered button can silently
  no-op for its row's current status.
- **Both `writeOrderStatusFragment` call sites** (local apply, primary-
  proxied replica) pass the same post-write `Status` semantics into the two
  new OOB writers; the JSON sync endpoint (`sync_orders.go`) never goes
  through this writer, so the machine-to-machine wire format is untouched.
- **Injection safety**: every interpolated value is escaped for its
  context on both the Go `fmt.Fprintf` path (receipt via
  `template.HTMLEscapeString`/`url.PathEscape`, labels via
  `HTMLEscapeString`, `TargetStatus` an internal constant) and the
  `html/template` path (contextual auto-escaping plus `jsonVals` for
  `hx-vals`).
- **i18n**: all four button-label keys (`orders.status.preparing/ready/
  collected`, `orders.btn.cancel`) already existed in every locale file
  pre-change — no new key, no `lang-pack-drift` follow-up needed.
- **RTL**: `.btn-actions` uses `display:flex`/`gap`/`justify-content` only
  — no physical `left`/`right`.
- No file-write/`paths.Data(...)` surface in this diff — the two recurring
  bug classes this pipeline watches for don't apply.
- No real client/shop name in test data (`R-00xx`/`V-10xx` only).
- Full gate re-run clean after the fix: `gofmt -l .` (none), `go build
  ./...`, `go vet ./...`, `go test ./...` (full suite), `golangci-lint run
  ./...` (0 issues), `guard-i18n.sh`, `guard-data-access.sh`,
  `guard-docs-shots.sh`, `guard-help-topics.sh`, `guard-compliance-claims.sh`,
  `guard-kiosk-engine.sh`, `guard-page-http-error.sh`,
  `guard-plugin-menu-read.sh` all pass.

### Flagged, accepted as-is (not blocking)

- **`web/help/en/order-status.md` was deliberately left unedited.** The
  existing prose (steps 5 and 7 in particular) stays accurate — nothing it
  says is contradicted by this change, and it was never wrong, only silent
  on the exact button-visibility mechanic, which is self-evident on
  screen. An attempted one-sentence addition was drafted and then reverted
  by Dev: this sandboxed session cannot reach the self-hosted Ollama
  translation endpoint (`192.168.1.231`, homelab-only), confirmed by a
  direct connection timeout, so adding the sentence to English only would
  have stranded fa/tr/ar with no CI signal (`guard-help-topics.sh` checks
  topic *existence* per locale, not content parity — ut-docs#1962, groomed
  this same cycle, is the card that closes exactly this gap generally).
  Reviewer agrees this is a reasonable call, not a corner cut. Filed as a
  follow-up rather than a merge blocker.
- **Minor UX note, shared-class design call, not this card's to make**:
  `.btn-actions` is `justify-content: center`, so the remaining buttons
  re-center horizontally after a tap removes one — a mild mis-tap risk on
  a touch till that didn't exist when every row always had all 4 in a
  fixed layout. Changing the shared class would also affect
  `catalog_row.html`/`tax_codes_table.html`. The real 1024×600 driven
  check (below) didn't surface this as a practical problem, and the change
  strictly reduces button count/width pressure versus before, so left
  as-is.
- `guard-deadcode-baseline.sh` could not run in the review's sandbox
  (missing GTK/WebKit dev headers `cmd/unitill-desktop` needs) —
  environmental, would fail identically on `main`, unrelated to this diff.

## Verified beyond automated tests

- Booted a real throwaway till (`UT_AUTH=off`, fresh temp data dir),
  seeded three real completed sales directly via the repo's own sqlite
  driver, and drove real HTTP POSTs against the running server — confirmed
  the exact button set at `new`/`preparing`/`ready` matches the design.
- Drove a **real Playwright/Chromium browser click** (not curl) on the
  "Ready" button for a `new` order: confirmed via the live DOM that both
  the status cell and the actions cell updated in one round trip
  (Preparing and Ready both gone, Collected/Cancel order remain) — proving
  the OOB swap actually works client-side, not just that the server emits
  the right markup.
- Drove a real click on "Collected": confirmed the pre-existing
  terminal-row-delete OOB behavior still fires correctly alongside the new
  actions-cell OOB swap.
- Visual check at the 1024×600 kiosk floor: `/orders` (3 rows across
  new/preparing/ready) and `/orders/{receipt}` (Collect + Refund), in
  **English (LTR)** and **Persian (fa, RTL)** — no overflow, correct RTL
  mirroring and translated labels, buttons visibly smaller than before and
  the row content stays dominant. Did **not** separately check dark/other
  theme variants or German/Turkish/Arabic label lengths — no new CSS was
  introduced (`.btn-actions` is pre-existing and already proven across
  themes/locales elsewhere), so this is assessed as low risk, not
  exhaustively verified.

## Safe-to-merge verdict

Yes. All gates green, TDD claims independently re-verified (including one
real gap found and fixed), real browser-driven behavior confirmed, no
compliance/i18n/data-access/kiosk-isolation issues.

## Deferred / follow-up

- Optional: a manual-doc mention of button-visibility-follows-status,
  translated properly once the self-hosted translation endpoint is
  reachable (or done interactively) — not filed as a separate card since
  ut-docs#1962 (translation-parity guard, groomed this cycle) is the
  systemic fix for this whole class of gap.
- The `.btn-actions` re-centering-on-tap UX note above, if it turns out to
  matter in practice on the pilot's real hardware.
