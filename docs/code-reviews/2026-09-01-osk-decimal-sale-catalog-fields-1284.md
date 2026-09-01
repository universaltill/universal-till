# Review: OSK decimal-corruption fix on sale-screen and catalog/variant fields (ut-docs#1284)

## What shipped

Same root cause already fixed in ut-docs#1249/#1272/#1275: `osk.js`'s
`insert()` does a naive `value += text` for `type="number"`/`type="email"`
fields (they expose no `setRangeText`/selection API), and a `type="number"`
input silently resets `.value` to `""` on any momentarily-invalid
intermediate float string while typing via the on-screen keyboard — so
`"2.50"` typed one keystroke at a time ends up `"50"`, not empty. This
card's independent review of #1275 found the same bug still live on more
fields, out of that card's five-admin-screen scope:

- `web/ui/pages/index.html` — split-tender `amount`/`change` (sale screen,
  p1: highest-OSK-exposure surface in the product)
- `web/ui/partials/basket.html` — basket `qty-input` (sale screen, p1;
  decimal pattern only when the line `.IsWeighed`, integer otherwise)
- `web/ui/pages/catalog.html` — `item-price` (p2, admin)
- `web/ui/partials/catalog_variants.html` — item `cost`, variant
  `price`/`cost` (both the existing-variant row and the "add variant"
  row), and modifier option `priceDeltaMajor` (both the existing-option
  row and the "add option" row) (p2, admin)

Fix, matching the established convention: `type="number"` →
`type="text" inputmode="decimal"` + a numeric `pattern` (via the
`moneypattern`/`moneyplaceholder` template helpers,
`internal/httpx/currency.go`, for money fields — see "What the review
found" below). No `oninput` handler was needed on any of these fields:
each one's existing conversion/read logic already operates on live
`.value` at submit time (`FormData` in `app.js`'s `addPayment()`, a
`submit`/`htmx:configRequest` listener, or server-side
`strconv.ParseFloat` on the raw posted string) rather than a stale
`oninput`-to-hidden-field copy osk.js's insert-only keystrokes could leave
behind.

## Separate finding, filed not fixed: ut-docs#1385

Diagnosis surfaced a more severe, independent bug: `#payment-overlay`
(the Tender panel hosting split-tender's `amount`/`change`) opens via
`showModal()` — a genuine native modal dialog, which makes the whole
document outside it, `#osk` included (a single instance appended once to
`<body>`, never re-parented into whichever dialog is open), inert per the
HTML living standard. A real tap on the on-screen keyboard cannot reach it
at all while the panel is open, independent of what `type=` its fields
use. Every other OSK-hosting dialog in this codebase (`#hold-modal`/
`#pfand-modal`/`#elevation-modal`/`#table-add-modal`) is deliberately
non-modal (`.show()`) for exactly this reason — `#payment-overlay` was
simply never given the same treatment. Filed as ut-docs#1385 (p1,
complexity:medium) with the exact fix recommendation; explicitly out of
scope here. Because of this, the new e2e spec's two split-tender tests
can't drive a real OSK button click while the panel is open — they call a
`simulateOskInsertViaField` helper that reproduces `osk.js`'s `insert()`
function body verbatim against the field instead (independently confirmed
below, not just asserted).

## Independent review

Opus, fresh context, isolated `git worktree` (per `reviewer` skill —
matches this card's `complexity:medium` routing: Sonnet built it, Opus
reviewed it). It **actually ran the gates itself** rather than reading the
diff: `gofmt -l .`, `go build ./...`, `go test ./...` (full, 42 packages),
the CI-blocking guards, the full e2e `--project=default` suite (268/268),
`make docs-shots` (92/92) — and independently re-verified the TDD claim
by reverting the four production files and re-running the new spec (5/5
failed with the exact diagnosed corruption), then restoring (5/5 passed,
20/20 under `--repeat-each=4`). It also independently confirmed, from
source and a live runtime probe (not on my word), both the `showModal()`
vs `.show()` claim for #1385 and that every fixed field's read logic
really does operate at submit-time on live `.value`.

**Verdict: not safe to merge as-is — one BLOCKING finding**, plus several
SHOULD-FIX items, all addressed in this same cycle (see below); the
production fix itself was found correct, well-reasoned, and fully green.

### BLOCKING — fixed

**`e2e/tests/hold-named-tab.spec.ts`**: the same `getByRole('textbox')
.first()` fragility this diff's own fix elsewhere exposed (a basket
`qty-input` moving from `type="number"`/ARIA role "spinbutton" to
`type="text"`/role "textbox" means a leftover basket line can now compete
with the scan field for `.first()`, and — DOM order — wins). This file's
"cancelling" test deliberately leaves a line in the basket and the next
test's `.first()` call then lands on it. The reviewer reproduced the live
consequence: a stray barcode string typed into the qty field, producing a
held sale with a `£6,000,000,000,014.40` total that persists for the rest
of the suite run (the test's own assertion is too loose to catch it, so
it was silently green). Fixed: scoped all three `.first()` calls in this
file to `.scan-row input[name="code"]` (same fix already applied to five
other files in this diff) and added a per-test basket reset — the file
had none.

The reviewer audited all 18 files using this pattern; only this one was a
genuine miss (the rest have explicit resets or the call only ever runs
against an empty basket).

### SHOULD-FIX — all addressed

1. **`basket.html`'s qty value didn't match its own new pattern.**
   `value="{{ printf "%.2f" .Qty }}"` always rendered 2 decimals, but a
   non-weighed line's `pattern="[0-9]+"` is integer-only — every
   non-weighed line was `:invalid` at rest (confirmed inert today: no
   `:invalid` CSS rule, htmx 1.9.1 doesn't validate this field, no
   wrapping `<form>`, so nothing currently depends on it — but a
   landmine). Fixed: render `%.0f` for a non-weighed line, `%.2f` only
   when `.IsWeighed`. This is also a small, correct UX improvement on its
   own (a countable item's qty column now reads "1", not "1.00").
   Updated two Go tests (`internal/pages/pos_scan_test.go`,
   `internal/pages/ui_smoke_test.go`) that hardcoded the old `"1.00"`/
   `"2.00"` rendering.
2. **`internal/pages/catalog/cost_currency_test.go`'s regression guard
   went vacuous.** It asserted `step="0.01"` was absent for a 0-decimal
   currency; `step` no longer exists on this field at all, so the
   assertion could never fail regardless of the fix's correctness.
   Rewrote it to assert the actual current invariant: `pattern="[0-9]+"`
   present, no `\.[0-9]` (decimal-permitting) pattern anywhere in the
   0-decimal-currency panel.
3. **8 sites hand-rolled the exact regex `internal/httpx/currency.go`'s
   `MoneyPattern`/`MoneyPatternAttr` exists for**, whose own doc comment
   warns this is precisely the bug class (a hardcoded `{1,2}` breaking the
   day a 3-decimal currency is added) a prior card (ut-docs#1274) already
   fixed once for `/100`. Converted all 8 (`index.html`'s amount/change,
   `catalog.html`'s item-price, `catalog_variants.html`'s cost/variant-
   price/variant-cost ×2/priceDeltaMajor ×2) to `{{ moneypattern
   currency.Decimals false }}` / `{{ moneyplaceholder currency.Decimals 0
   }}`, matching the convention `shifts.html`/`reports_tab_tips.html`
   already use.
4. **The new spec used the exact `getByRole('textbox').first()` pattern
   this diff exists to fix**, safe only via an `afterEach` reset that
   itself swallowed errors — inconsistent with the five comments the same
   commit added elsewhere. Scoped all three occurrences to `.scan-row
   input[name="code"]`.
5. **Coverage was inverted relative to the issue's own named scope.** The
   original test only exercised the "add variant"/"add option" (`vf-new`)
   rows, which the issue never named — the rows it DID name (the
   existing-variant `vf-{{.ID}}` row, the existing modifier-option's own
   delta field) had no coverage, and the reviewer's revert run showed the
   combined test failing at its *first* assertion, leaving the later
   fields' TDD claim unproven. Split the single test into four
   independent tests: item cost (the field the issue names at line ~37),
   the EXISTING variant row's price/cost, the EXISTING option's
   price-delta, and — kept, since it's real, adjacent, same-bug-class
   coverage, just correctly labeled as the extra scope it is — the
   new-variant/new-option rows. Each is now independently
   revert-verifiable.
6. **Observed flake (once, not reproduced in `--repeat-each=4` or the
   full suite).** The combined test's two htmx submits had no
   `waitForResponse` guard, unlike every scan elsewhere in this file,
   leaning on the 5s `expect` auto-retry. Added explicit
   `waitForResponse` on both the item-save and the new-group-save
   requests in `createProbeItemAndOpenVariants`/the option test.

### NICE-TO-HAVE — addressed

- `internal/pages/catalog/handlers.go`'s comment still referenced the
  field's now-removed `step=""` attribute — updated to `pattern=""`.

### NICE-TO-HAVE — noted, not actioned

- Losing `type="number"` also loses spinner arrows / arrow-key / wheel
  increment on desktop tills — accepted tradeoff already made by
  #1249/#1272/#1275, noted for the record only.
- Heads-up for #1385: once the OSK can reach the tender dialog, its ↵
  key calls `form.requestSubmit()` on `#split-tender-form`, which has no
  submit handler/`action` — that becomes a native GET navigation off the
  sale screen. Pre-existing, unrelated to this diff; left for #1385's own
  fix to account for.

## Verified beyond automated tests

- **TDD claim, independently re-verified twice** (once by me — the
  session that wrote this fix — using `git show origin/main:<path>` to
  restore the true pre-fix markup rather than a WIP-commit snapshot that
  already contained the fix; once by the independent Opus reviewer in an
  isolated worktree): all 8 new-spec tests fail with the exact diagnosed
  corruption against the unfixed markup (`"2.50"`→`"50"`,
  `"4.20"`→`"20"`, etc.), pass restored.
- `make docs-shots` regenerated: only `web/help/img/manifest.json`'s
  surface hash and `sell.png` (all 4 locales) changed — the qty-column
  format fix (#1 above) is the one genuinely visible change; every other
  field's `type="number"`→`type="text"` swap renders byte-identically in
  a headless, un-hovered screenshot (no spinner arrows to lose visually
  there). No manual (`web/help/`) prose update needed — this is an
  invisible-to-the-shop-owner bugfix plus one cosmetic qty-format
  improvement already covered by the regenerated screenshot, not a new
  page or changed workflow.
- Full `go test ./...` (all packages) and the full e2e `--project=default`
  suite (271 tests) both green after every review fix, not just the
  isolated new spec.
- All 16 CI-blocking guards pass, including `guard-docs-shots.sh`
  (surface hash matches the regenerated manifest) and `guard-i18n.sh`
  (no new user-facing strings — markup attribute changes only).

## Safe to merge

Yes. One blocking finding and five should-fix items from independent
review, all fixed and re-verified; full gate green; TDD claim confirmed
twice, independently.

## Explicitly deferred (new Backlog cards)

- **ut-docs#1385** (p1, complexity:medium) — `#payment-overlay`'s
  `showModal()` makes the on-screen keyboard unreachable for split-tender's
  fields on real kiosk hardware, independent of field type. Recommended
  fix: mirror `#hold-modal`/`#pfand-modal`/`#elevation-modal`/
  `#table-add-modal`'s established `.show()` pattern.
