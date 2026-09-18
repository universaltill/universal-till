# Code review — basket qty +/- stepper buttons (ut-docs#2217)

- **Date:** 2026-09-18
- **Ticket:** ut-docs#2217 — dedicated +/- stepper buttons as the real 44px
  touch-target fix for the basket's per-line qty control (the approach
  ut-docs#1340 decided on, after rejecting resizing `.qty-input` itself).
- **Branch:** `feat/2217-basket-qty-steppers`
- **Reviewed commit:** `892842f` (rebased onto `main` `f7da277`).
- **Reviewer:** independent pass, different-model subagent working only from the
  diff — never saw the implementation reasoning.
- **Verdict: SAFE TO MERGE**, after the one blocker found here was fixed in
  this branch (regenerated manual screenshots, commit `0274621`).

> **Note on scope — two versions were reviewed.** This review began against an
> earlier commit of the same branch, `d0af3bd`, which used a **flanking**
> stepper layout (buttons either side of `.qty-input`, qty column widened
> `4.3rem → 8.4rem`). I found that version **re-broke ut-docs#1314** and
> recorded it as a blocker. The branch was then amended to `892842f`, which
> replaces flanking with a **stacked** row and reverts the column to `4.3rem`.
> The findings below are against `892842f`; the superseded finding is kept at
> the end for the record, because it is the reason the current design is
> shaped the way it is.

---

## What shipped (`892842f`)

- `internal/pages/pos_api.go:1067-1092` — `/api/pos/line` now accepts an
  optional relative `delta` form param alongside the existing absolute `qty`.
  When `delta` and `key` are both present it looks the line's current qty up
  from `d.Engine.Basket().Lines`, adds the delta and clamps at 0; a malformed
  delta is a 400. Mirrors the already-shipped `/api/self-order/line`.
- `internal/pages/pos_api_test.go:216-325` — four new tests
  (`DeltaIncrementsAndDecrementsQty`, `DeltaClampsAtZeroAndVoids`,
  `DeltaPreservesDiscount`, `InvalidDeltaRejected`).
- `web/ui/partials/basket.html:166-214` — a new `.qty-stepper` row (`:197`) **below**
  `.qty-input`, holding the `−` and `+` buttons side by side; each
  `hx-post="/api/pos/line"` with a `delta` and `hx-include="closest tr"`.
- `web/public/app.css:2525-2526` — `.qty-stepper { display: flex; width: 3.4rem;
  gap: .2rem }` and `.qty-step-btn { flex: 1; min-height: 2.6rem; … }`. The
  stepper is exactly as wide as `.qty-input`/`.disc-input` already were, so
  **the qty column's reserved width is unchanged at `4.3rem`** (app.css:2678).
  Buttons are hidden at the `≤480px` phone tier (app.css:2811).
- `web/locales/{en,ar,fa,tr}.json` — `basket.qty.decrease` / `basket.qty.increase`.
- `web/help/en/sell.md:19` — step 3 reworded to mention the new buttons.
- `web/help/img/{en,ar,fa,tr}/sell.png` + `manifest.json` — regenerated during
  this review (see Blocker 1).

---

## Findings

### BLOCKER 1 — `guard-docs-shots.sh` was red (CI-blocking) — **FIXED in this branch**
`web/help/img/**`, `web/help/en/sell.md:19`

```
guard-docs-shots: the app surface (web/ui/**, web/public/**, or internal/pages/**.go)
  changed since the manual's screenshots were last taken.
guard-docs-shots: topic markdown changed since its screenshot was taken (locale/topic):
  - en/sell
```

This guard runs in `ci.yml`'s `build` job, so the branch would have gone red on
push. The sell screen genuinely gained a visible control, so this needed a real
`make docs-shots`, not the `update-docs-shots-surface-hash.sh` escape hatch.
CLAUDE.md's "the user manual ships with the feature, not after it" applies.

**Fixed here** (commit `0274621`): ran `make docs-shots`, which passed (124
shots) and touched exactly the four `sell.png` locales plus `manifest.json` —
confirming sell is the only surface whose pixels moved. Guard now green
(surface `4d756a017baf`). Committed unmodified.

### SHOULD-FIX 2 — non-finite `delta` corrupts the basket totals
`internal/pages/pos_api.go:1075`

`strconv.ParseFloat` accepts `"NaN"`, `"Inf"`, `"-Inf"`, and the clamp
`if qty < 0` is **false for NaN**. Measured against the real handler:

| `delta=` | status | result |
|---|---|---|
| `NaN` | **200** | line kept, `Qty=NaN`, lineTotal/subtotal/**total = 0.00** |
| `Inf` | **200** | line kept, `Qty=+Inf`, lineTotal/subtotal/**total = 0.00** |
| `1e400` | 400 | correctly rejected (range error) |

So a basket visibly holding items reports `Total: £0.00`. The absolute `qty`
path is partly protected by accident — `f >= 0` is false for NaN — but
`qty=Inf` has the same hole and is **pre-existing**, so only the NaN case is
new here.

Not a blocker: `/api/pos/line` is **authenticated**
(`internal/auth/middleware.go:189` exempts only `/self-order` and
`/api/self-order/*`), and the shipped buttons only ever send the literals
`-1`/`1`, so it is not reachable from the UI.

**Deliberately not fixed here.** The delta block is a field-for-field mirror of
`/api/self-order/line` (`internal/pages/self_order_shop.go:308-322`), which has
the identical gap and, unlike this one, **is anonymous-LAN-reachable**.
Hardening only the POS side would break the symmetry both files' comments lean
on while leaving the more exposed twin unguarded. Worth its own card covering
both, adding `math.IsNaN(delta) || math.IsInf(delta, 0)` to the existing 400
branch in each. CLAUDE.md's "validate all external input" applies.

### SHOULD-FIX 3 — manual describes buttons a phone user cannot see
`web/help/en/sell.md:19`

The new prose is unconditional: *"Adjust a line's quantity with the **−**/**+**
buttons beside it"*. At `≤480px` those buttons are `display: none`
(app.css:2811). On a phone the manual describes a control that isn't there.
Needs a width caveat, or the phone-tier follow-up landing first.

Minor wording nit in the same sentence: the buttons are now *below* the qty
box, not "beside it" / "between them" — the prose still describes the
superseded flanking layout.

### NON-BLOCKING 4 — new tests index `Lines[0]` unguarded, so a regression panics
`internal/pages/pos_api_test.go:233`, `:241`, `:284`, `:301`

`dp.Engine.Basket().Lines[0]` with no length check. When the handler
regresses, qty defaults to 0 → the line is voided → `Lines` is empty → the
test dies with `panic: runtime error: index out of range [0] with length 0`,
which **aborts the whole package test binary**. In my revert run this masked
the other three new tests entirely — only one failure was reported.

Inconsistent within the same diff: `DeltaClampsAtZeroAndVoids` (`:260`) does
check `len(...)` first. A `t.Fatalf` guard before each index would make a real
regression readable. (The pre-existing
`TestLineHandler_QtyChangeDoesNotClearDiscount` has the same habit, so this is
a house pattern, not a new sin — hence non-blocking.)

### NON-BLOCKING 5 — `delta` silently ignored on the `code`-addressed branch
`internal/pages/pos_api.go:1074`

The guard is `v != "" && key != ""`. A POST carrying `delta` + `code` (no
`key`) falls through to the absolute-`qty` branch and silently applies whatever
`qty` happened to be included, rather than 400-ing. The comment promises "an
invalid delta is a real 400 … not a silent no-op"; this is the one path where
that isn't true. Unreachable from the template (the buttons always send `key`
via `hx-vals`), and arguably intentional ("key-addressed only"), but the
comment overstates the guarantee.

### NON-BLOCKING 6 — `lang-pack-drift` follow-up owed
`web/locales/en.json:113-114`

Two new keys need follow-up PRs in `ut-plugin-language-{de,es}`. Advisory-only
on the PR, **blocking on push to `main`** — worth doing before merge.

---

## What I verified personally

Everything below I ran in my own worktree against `892842f`, not read.

| Check | Result |
|---|---|
| `go build ./...` | clean |
| `go vet ./internal/pages/...` | clean |
| `gofmt -l .` | no output |
| `go test ./internal/pages/... ./internal/ui/...` | **all ok** |
| `golangci-lint run ./internal/pages/...` | **0 issues** |
| `guard-i18n.sh` | green — 1731 keys resolve, all locales match `en.json` |
| `guard-data-access.sh` | green — no SQL outside `internal/data`/`internal/db` |
| `guard-kiosk-engine.sh` | green |
| `guard-help-topics.sh` | green |
| `guard-help-drift.sh` | green (exit 0; `sell` drift unchanged in size — the edit is prose inside an existing numbered step, so no structural counts moved) |
| `guard-compliance-claims.sh` | green — 343 files |
| `guard-htmx-loaded.sh` | green |
| `guard-docs-shots.sh` | red → **green after the fix committed here** |

### E2E — the layout specs that matter for this change

This change trades horizontal space for **vertical** space, so the row-height
ACs are the ones at risk. All run serially, `--workers=1`:

| Spec | Result |
|---|---|
| `basket-item-name-width-1314.spec.ts` (1024x600 + 1280x800) | **4/4 pass** |
| `sale-screen-213.spec.ts` — incl. `>=4 basket lines visible without scrolling at 1280x800` | **8/8 pass** |
| `sale-screen-213.spec.ts` — kiosk `body.kiosk` 1024x600 row-count floor (ut-docs#1339) | pass |
| `basket-item-name-phone-tier-1338.spec.ts` | **7/7 pass** |

So the height cost is real but stays inside the existing budget: the ≥4-lines
AC and the kiosk floor both still hold.

**One caveat I could not fully clear.** `basket-no-horizontal-scroll-391.spec.ts`
and `ui-scale-basket.spec.ts` were **flaky in my sandbox** — the failing subset
shuffled between runs, failures were `element(s) not found` / `ECONNREFUSED
127.0.0.1:909x` (worker-server startup, which then breaks `afterEach`'s
`/api/pos/reset` and contaminates later tests) rather than geometry assertions,
and `ui_scale 2` passed while `ui_scale 1` failed — backwards for a real layout
regression. A control run on **`main`** (`f7da277`, change absent) also failed
one of them, so there is a pre-existing/environmental component. I do **not**
attribute these to this change, but CI should be the arbiter.

### Revert-then-restore TDD verification (`TestLineHandler_DeltaPreservesDiscount`)

Done personally. I reverted **only** the `delta` block in `pos_api.go` back to
the pre-diff `if v := r.Form.Get("qty"); v != ""` and re-ran:

- `TestLineHandler_DeltaPreservesDiscount` — **FAILED** at `pos_api_test.go:301`
- `TestLineHandler_DeltaIncrementsAndDecrementsQty` — **FAILED** at `:233`

The failure is causally correct, not incidental: with the block gone the
`delta` param is ignored, no `qty` is posted, `qty` defaults to `0.0`,
`UpdateLineByKey` voids the line, and `Lines` is empty. It surfaces as a panic
rather than an assertion (Finding 4), but it is the right cause.

Restored, re-ran, and **all 7 `TestLineHandler_*` pass** including the 4 new
ones. **These are real tests, not false-passes.**

### Design-claim spot-checks (each asserted in the diff, each confirmed)

- **`hx-include="closest tr"` on both buttons** — yes, `basket.html:201` (`−`)
  and `:209` (`+`). Load-bearing exactly as claimed: `/api/pos/line` parses
  `discount` from the form on *every* request and defaults it to `0`
  (`pos_api.go:1094-1099`), then passes it straight to
  `UpdateLineByKey(key, qty, money.FromMinor(discount))`. Remove the
  `hx-include` and a step posts no `discount`, so any line discount is silently
  wiped. Correctly identified and correctly tested.
- **`.btn-touch` height-only precedent** — accurate. `app.css:579` is
  `min-height: 46px` with no `min-width`. The claim checks out, and the stacked
  layout leans on it harder than the flanking one did: at `flex: 1` inside a
  `3.4rem` row with a `.2rem` gap each button is only ~1.6rem (~27px) wide.
- **44px target** — `2.6rem` against `html { font-size: calc(var(--ui-scale,1)
  * var(--fluid-fs)) }` with `--fluid-fs: clamp(17px, …, 20px)` (app.css:105)
  ⇒ ≥44.2px at the floor, larger at any `ui_scale > 1`. Correct.
- **"Zero column-width change from pre-#2217"** — confirmed:
  `app.css:2678` is `width: 4.3rem`, identical to `main`.
- **Phone-tier scoping** — correct; `.qty-step-btn { display: none }` sits
  inside `@media (max-width: 480px)`, and `1338`'s own spec passes.
- **Weighed items get the same ±1** — confirmed, `/api/self-order/line` has no
  `IsWeighed` check either (`self_order_shop.go:308-322`). Precedent accurate.

### Visual check

Looked at the regenerated `web/help/img/en/sell.png` (1024x600, the product's
reference kiosk viewport) rather than trusting the specs alone: item names
render in full (`Coca-Cola Can 330ml`, `Pepsi Can 330ml`), the `−`/`+` row sits
cleanly between the qty and discount boxes, and nothing overlaps PRICE/TOTAL.
Row height roughly doubles (~57px → ~118px), which is the deliberate trade and
is what the ≥4-lines AC above bounds.

### Standing checks

- **Repository pattern** — no SQL added anywhere (guard green); pure
  handler/template/CSS change.
- **Money** — handler still converts only at the boundary via
  `money.FromMinor(discount)`; tests read `.Minor()` at assertion boundaries.
  `qty` stays `float64` (a quantity, correctly *not* money). No violations.
- **Offline-first** — nothing added touches the network; the delta path reads
  the in-process engine only. Checkout remains offline-capable.
- **`os.MkdirAll` bug class** — N/A, confirmed: no file writes in the diff.
- **`paths.Data(...)` bug class** — N/A, confirmed: no filesystem paths at all.
- **Secrets / real client names** — none. Test data is the existing
  `ABC` / `5000000000104` catalog fixtures.
- **RTL** — clean. The new rules use only `display/flex/gap/width/min-height/
  padding/font-size/line-height/border-radius`; no physical `left`/`right`
  anywhere (matches only in comment prose). The stepper mirrors correctly by
  virtue of being a plain flex row.
- **i18n** — both keys present in all four `web/locales/*.json` with real
  translations (not English placeholders); `aria-label`s go through `T`. The
  bare `−`/`+` glyphs are symbols, consistent with the existing `✕` remove
  button, and `guard-i18n.sh` is green.

---

## Superseded finding (against `d0af3bd`, kept for the record)

The earlier flanking-stepper version widened the qty column `4.3rem → 8.4rem`
(`app.css:2661`). Because ITEM is the only column with no declared width, under
`table-layout: fixed` it absorbed the entire 4.1rem — and `.line-item`'s
`max-width: 10.4rem` is a **ceiling, not a floor**, so nothing stopped the
collapse. `basket-item-name-width-1314.spec.ts` failed **4/4**:

| viewport | `.line-name` rendered width | clamp overflow |
|---|---|---|
| 1024x600 | **38px** | scrollHeight 90 vs clientHeight 36 |
| 1280x800 | **40.0px** | scrollHeight 94 vs clientHeight 38 |

`"Cheddar Cheese 400g"` needed ~5 lines in a 2-line clamp; the screenshot
showed `Coca -C…` / `Peps i C…`. Causation was proven by reverting only that
one line — the same 4 tests went green.

The current `892842f` design (stacked row, column unchanged) resolves this
completely, and its CSS/template comments now cite that spec directly as the
reason for the shape. Worth preserving the lesson: the arithmetic in the
flanking version's comments was internally correct; what was missing was that
the ITEM column had no slack left to fund it.

---

## Verdict

**SAFE TO MERGE.**

One blocker was found and fixed in-branch (stale manual screenshots, a
CI-blocking guard). The handler logic, its tests, i18n, RTL handling and
touch-target arithmetic are all sound, and the vertical cost of the stacked
stepper is bounded by the existing ut-docs#213 / #1339 row-count ACs, both
re-run and passing.

Recommended follow-ups, neither blocking: Finding 2 (non-finite `delta`,
covering both the POS and self-order twins — the self-order one is the more
exposed of the two) and Finding 3 (phone-tier manual caveat + "beside it"
wording now that the buttons sit below).
