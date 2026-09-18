# Code review — basket qty +/- stepper buttons (ut-docs#2217)

- **Date:** 2026-09-18
- **Ticket:** ut-docs#2217 — dedicated +/- stepper buttons as the real 44px
  touch-target fix for the basket's per-line qty control (the approach
  ut-docs#1340 decided on, after rejecting resizing `.qty-input` itself).
- **Branch:** `feat/2217-basket-qty-steppers` (one commit, `d0af3bd`, on top of
  `main` at `f7da277`).
- **Reviewer:** independent pass, different-model subagent working only from the
  diff — never saw the implementation reasoning.
- **Verdict: BLOCKER FOUND — DO NOT MERGE.** The change re-breaks
  ut-docs#1314's basket item-name legibility at **both** supported till
  viewports. The repo's own existing regression spec for exactly this
  (`e2e/tests/basket-item-name-width-1314.spec.ts`) fails 4/4 on this branch
  and passes 4/4 with one line reverted. A second, independent blocker: the
  CI-blocking `guard-docs-shots.sh` is red on this branch.

The Go/handler half of this change is well-built and I found no correctness bug
in it. The blocker is entirely in the CSS column budget.

---

## What shipped

- `internal/pages/pos_api.go:1067-1092` — `/api/pos/line` now accepts an
  optional relative `delta` form param alongside the existing absolute `qty`.
  When `delta` and `key` are both present it looks the line's current qty up
  from `d.Engine.Basket().Lines`, adds the delta and clamps at 0; a malformed
  delta is a 400. Mirrors the already-shipped `/api/self-order/line`.
- `internal/pages/pos_api_test.go:216-325` — four new tests
  (`DeltaIncrementsAndDecrementsQty`, `DeltaClampsAtZeroAndVoids`,
  `DeltaPreservesDiscount`, `InvalidDeltaRejected`).
- `web/ui/partials/basket.html:143-199` — `.qty-input` wrapped in a new
  `.qty-stepper` flex row (opens at `:160`) with a `−` button before and `+`
  after, each `hx-post="/api/pos/line"` + `hx-include="closest tr"`.
- `web/public/app.css` — new `.qty-stepper`/`.qty-step-btn` rules (2506-2507);
  basket qty column `4.3rem` → `8.4rem` (2661); phone-tier (`≤480px`) overrides
  hiding the buttons and reverting the column to `4.3rem` (2803-2804).
- `web/locales/{en,ar,fa,tr}.json` — `basket.qty.decrease` / `basket.qty.increase`.
- `web/help/en/sell.md:19` — step 3 reworded to mention the new buttons.

---

## Findings

### BLOCKER 1 — item-name legibility regression at both till viewports
`web/public/app.css:2661`

Widening the qty column `4.3rem → 8.4rem` takes **4.1rem straight out of the
ITEM column**. ITEM is the one column with no declared width, so under
`table-layout: fixed` it absorbs 100% of the change (the diff's own comment at
2618-2632 correctly identifies this mechanism — it just under-estimates how
much headroom was left).

`.line-item`'s `max-width: 10.4rem` (app.css:2072) is a **ceiling, not a
floor**, so nothing stops the collapse.

Measured, not eyeballed — `e2e/tests/basket-item-name-width-1314.spec.ts`,
the spec that exists specifically to prevent this:

| viewport | result | `.line-name` rendered width | clamp overflow |
|---|---|---|---|
| 1024x600 (kiosk floor) | **FAIL** | **38px** | scrollHeight 90 vs clientHeight 36 |
| 1280x800 (default till) | **FAIL** | **40.0px** | scrollHeight 94 vs clientHeight 38 |

All 4 tests in that file fail. `"Cheddar Cheese 400g"` needs ~5 lines in a
2-line clamp; the visible result is `Coca -C…` / `Peps i C…` — roughly four
characters per line, mid-word, ellipsised.

**Causation proven**, not inferred: I reverted only `8.4rem` → `4.3rem` on
line 2661, changing nothing else, and the same 4 tests went green
(27.5s run). Restored afterwards.

This is the exact failure mode the commit message and the CSS comment say
the design avoided. The reasoning there is sound for the *square*-button
version it rejected (5.9rem stolen); it just doesn't follow that the
1.8rem-wide version's 4.1rem is affordable. It isn't — at any supported
viewport. The dev's own live verification measured **button** bounding boxes
(≥44.19px tall, which I confirm is correct) but not the item column those
buttons were taking width from, and this existing spec was not run.

Note the shape of the evidence: the phone tier (≤480px) was already scoped
out for precisely this reason. With 1024x600 and 1280x800 now also shown not
to fit, the stepper-flanking-the-input layout does not fit **any** supported
viewport's basket budget. That makes this a design question, not a CSS tweak.

**Not fixed here — deliberately.** Every candidate (drop `.qty-input` for
non-weighed lines; stack the steppers on their own row against #1340's
77-79px height budget; shrink the input; widen the basket panel) is a real
product/UX trade-off of the kind that already took four documented attempts
in this column's history. Guessing one in review is how #1314/#1338 got
re-broken in the first place. Back to Architect/UX.

### BLOCKER 2 — `guard-docs-shots.sh` is red (CI-blocking)
`web/help/img/**`, `web/help/en/sell.md:19`

```
guard-docs-shots: the app surface (web/ui/**, web/public/**, or internal/pages/**.go)
  changed since the manual's screenshots were last taken.
guard-docs-shots: topic markdown changed since its screenshot was taken (locale/topic):
  - en/sell
guard-docs-shots: run `make docs-shots` and commit the result
```

This guard runs in `ci.yml`'s `build` job, so the branch goes red on push
regardless of Blocker 1. The sell screen genuinely changed pixels (visible new
buttons), so this needs a real `make docs-shots`, not the
`update-docs-shots-surface-hash.sh` escape hatch.

I ran `make docs-shots` myself to confirm it is reproducible here: it passed
(124 shots, 2.4m) and produced exactly `web/help/img/{en,ar,fa,tr}/sell.png`
plus `manifest.json` — confirming sell is the only surface whose pixels moved.
**I reverted that regeneration rather than committing it**, because the
screenshots it produces are pictures of Blocker 1 (that regeneration is in
fact how I first saw the regression). Regenerate after the layout is fixed.

### SHOULD-FIX 3 — non-finite `delta` corrupts the basket totals
`internal/pages/pos_api.go:1075`

`strconv.ParseFloat` accepts `"NaN"`, `"Inf"`, `"-Inf"`, and the clamp
`if qty < 0` is **false for NaN**. Measured against the real handler:

| `delta=` | status | result |
|---|---|---|
| `NaN` | **200** | line kept, `Qty=NaN`, lineTotal/subtotal/**total = 0.00** |
| `Inf` | **200** | line kept, `Qty=+Inf`, lineTotal/subtotal/**total = 0.00** |
| `1e400` | 400 | correctly rejected (range error) |

So a basket visibly holding items reports a `Total: £0.00`. The absolute
`qty` path is partly protected by accident — `f >= 0` is false for NaN — but
`qty=Inf` has the same hole and is **pre-existing**, so only the NaN case is
new here.

Severity held at should-fix, not blocker: `/api/pos/line` is
**authenticated** (`internal/auth/middleware.go:189` exempts only
`/self-order` and `/api/self-order/*`), and the shipped buttons only ever
send the literals `-1`/`1`, so this is not reachable from the UI.

**Not fixed here**, on purpose: the delta block is a deliberate
field-for-field mirror of `/api/self-order/line`
(`internal/pages/self_order_shop.go:308-322`), which has the identical gap
and, unlike this one, **is anonymous-LAN-reachable**. Hardening only the POS
side would silently break the symmetry both files' comments lean on, while
leaving the more exposed twin unguarded. Worth its own card covering both,
adding `math.IsNaN(delta) || math.IsInf(delta, 0)` to the existing 400 branch
in each. CLAUDE.md's "validate all external input" applies.

### SHOULD-FIX 4 — manual describes buttons a phone user cannot see
`web/help/en/sell.md:19`

The new prose is unconditional: *"Adjust a line's quantity with the **−**/**+**
buttons beside it"*. At `≤480px` those buttons are `display: none`
(app.css:2803). On a phone the manual now describes a control that isn't
there. Needs a width caveat, or the phone-tier follow-up landing first.

### NON-BLOCKING 5 — new tests index `Lines[0]` unguarded, so a regression panics
`internal/pages/pos_api_test.go:233`, `:241`, `:284`, `:301`

`dp.Engine.Basket().Lines[0]` with no length check. When the handler
regresses, qty defaults to 0 → the line is voided → `Lines` is empty → the
test dies with `panic: runtime error: index out of range [0] with length 0`,
which **aborts the whole package test binary**. In my revert run this masked
the other three new tests entirely — only one failure was reported.

Inconsistent within the same diff: `DeltaClampsAtZeroAndVoids`
(`:260`) does check `len(...)` first. A `t.Fatalf` guard before each index
would make a real regression readable. (The pre-existing
`TestLineHandler_QtyChangeDoesNotClearDiscount` has the same habit, so this
is a house pattern, not a new sin — hence non-blocking.)

### NON-BLOCKING 6 — `delta` silently ignored on the `code`-addressed branch
`internal/pages/pos_api.go:1074`

The guard is `v != "" && key != ""`. A POST carrying `delta` + `code` (no
`key`) falls through to the absolute-`qty` branch and silently applies
whatever `qty` happened to be included, rather than 400-ing. The comment
promises "an invalid delta is a real 400 … not a silent no-op"; this is the
one path where that isn't true. Unreachable from the template (the buttons
always send `key` via `hx-vals`), and arguably intentional
("key-addressed only"), but the comment overstates the guarantee.

### NON-BLOCKING 7 — `lang-pack-drift` follow-up owed
`web/locales/en.json:113-114`

Two new keys need follow-up PRs in `ut-plugin-language-{de,es}`. Advisory-only
on the PR, **blocking on push to `main`** — worth doing before merge.

---

## What I verified personally

Everything below I ran in my own worktree, not read.

| Check | Result |
|---|---|
| `go build ./...` | clean |
| `go vet ./internal/pages/...` | clean |
| `gofmt -l .` | no output |
| `go test ./internal/pages/... ./internal/ui/...` | **all ok** (pages 214.9s) |
| `golangci-lint run ./internal/pages/...` | **0 issues** |
| `guard-i18n.sh` | green — 1731 keys resolve, all locales match `en.json` |
| `guard-data-access.sh` | green — no SQL outside `internal/data`/`internal/db` |
| `guard-kiosk-engine.sh` | green |
| `guard-help-topics.sh` | green |
| `guard-help-drift.sh` | green (exit 0; `sell` drift unchanged in size — the edit is prose inside an existing numbered step, so no structural counts moved) |
| `guard-compliance-claims.sh` | green — 343 files |
| `guard-htmx-loaded.sh` | green |
| **`guard-docs-shots.sh`** | **RED — Blocker 2** |
| **`e2e/tests/basket-item-name-width-1314.spec.ts`** | **4/4 FAIL — Blocker 1** |

### Revert-then-restore TDD verification (`TestLineHandler_DeltaPreservesDiscount`)

Done personally, as required. I reverted **only** the `delta` block in
`pos_api.go` back to the pre-diff `if v := r.Form.Get("qty"); v != ""` and
re-ran:

- `TestLineHandler_DeltaPreservesDiscount` — **FAILED** at `pos_api_test.go:301`
- `TestLineHandler_DeltaIncrementsAndDecrementsQty` — **FAILED** at `:233`

The failure is causally correct, not incidental: with the block gone the
`delta` param is ignored, no `qty` is posted, `qty` defaults to `0.0`,
`UpdateLineByKey` voids the line, and `Lines` is empty. It surfaces as a
panic rather than an assertion (Finding 5), but it is the right cause.

Restored from my backup, re-ran, and **all 7 `TestLineHandler_*` pass**
including the 4 new ones. Confirmed `git status` clean afterwards.
**These are real tests, not false-passes.**

### Design-claim spot-checks (each asserted in the diff, each confirmed)

- **`hx-include="closest tr"` on both buttons** — yes, `basket.html:164` (`−`)
  and `:195` (`+`). Load-bearing exactly as claimed: `/api/pos/line` parses `discount`
  from the form on *every* request and defaults it to `0`
  (`pos_api.go:1094-1099`), then passes it straight to
  `UpdateLineByKey(key, qty, money.FromMinor(discount))`. Remove the
  `hx-include` and a step posts no `discount`, so any line discount is
  silently wiped. Correctly identified and correctly tested.
- **`.btn-touch` height-only precedent** — accurate. `app.css:579` is
  `min-height: 46px` with no `min-width`. The claim checks out.
- **44px target** — `2.6rem` against `html { font-size: calc(var(--ui-scale,1)
  * var(--fluid-fs)) }` with `--fluid-fs: clamp(17px, …, 20px)` (app.css:105)
  ⇒ ≥44.2px at the floor, larger at any `ui_scale > 1`. Correct.
- **Stepper row arithmetic** — `2×1.8 + 3.4 + 2×0.2 = 7.4rem`. The
  `+ .35rem*2` cell padding is right: `.basket td`'s effective padding is
  `.4rem .35rem` from app.css:2705, a later top-level rule that overrides
  line 2659's `.55rem .6rem` at equal specificity (not the `.6rem` a reader
  might assume). `7.4 + .7 + .3 headroom = 8.4rem` is **internally
  consistent**. The column just cannot afford 8.4rem — Blocker 1 is about the
  budget, not the arithmetic.
- **Phone-tier scoping** — correct. Both overrides sit at brace depth 1
  inside `@media (max-width: 480px)` (opens app.css:2770), and being later in
  the cascade at equal specificity they beat the 8.4rem rule. Not silently
  broken.
- **Weighed items get the same ±1** — confirmed, `/api/self-order/line` has no
  `IsWeighed` check either (`self_order_shop.go:308-322`). Precedent accurate.

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
- **RTL** — clean. The new rules use only `display/align-items/gap/min-width/
  min-height/padding/font-size/line-height/border-radius/width`; no physical
  `left`/`right` anywhere (matches only in comment prose). The stepper mirrors
  correctly by virtue of being a plain flex row.
- **i18n** — both keys present in all four `web/locales/*.json` with real
  translations (not English placeholders); `aria-label`s go through `T`. The
  bare `−`/`+` glyphs are symbols, consistent with the existing `✕` remove
  button, and `guard-i18n.sh` is green.

---

## Verdict

**BLOCKER FOUND — do not merge.**

Two independent blockers: a user-visible regression that makes basket item
names unreadable on the primary checkout screen at every supported till
viewport (Blocker 1), and a red CI-blocking guard (Blocker 2).

Blocker 1 needs a design decision, not a patch, so I have deliberately left
the code untouched and my worktree diff is the dev's commit plus this record.
The handler, its tests, the i18n, the RTL handling and the touch-target
arithmetic are all sound and should survive whatever layout lands — the
rework is confined to how the stepper earns its width.

Recommended follow-ups once the layout is resolved: Finding 3 (non-finite
`delta`, covering both twins) and Finding 4 (phone-tier manual caveat).
