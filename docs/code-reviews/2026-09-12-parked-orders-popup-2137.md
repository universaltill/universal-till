# Code review: parked-orders popup beside Card (ut-docs#2137)

**Date:** 2026-09-12
**Card:** universaltill/ut-docs#2137
**Branch:** `feat/2137-parked-orders-popup`
**Complexity:** medium (Dev: Opus inline, Review: Fable subagent in an isolated worktree)

## What shipped

A parked order was **unreachable** on the pilot tablet. Two separately-reasonable
decisions combined:

1. `/open-orders` is read-only by design (ut-docs#1918), and rendered a hint:
   *"To pick an order back up, tap it on the On hold strip on the sale screen."*
2. That strip is clipped off-screen at 1280×800 (ut-docs#2128) — its CSS budget
   was measured at 1024×600.

So the page pointed at a control the device does not show. Confirmed on the
device by screenshot rather than over the API: the strip **does** render — four
chips, server-side, under *"Geparkt:"* — it is simply not on screen. Three real
orders were stranded, one open **221 minutes**.

Reported by the product owner in two messages: *"the open orders are not
tapable"*, then *"on the sale screen there is no held sale to select"*. The fix
is his own design: *"it can be a button to show the list of holds in a popup"*,
*"next to the card button"*.

A button in the quick-pay row beside Card opens a popup listing every parked
order, each a full-width touch target that resumes it. It reuses
`POST /api/pos/resume` with `hx-target="#basket"` exactly as the strip's chips
do, so the empty-basket rule stays enforced in the endpoint rather than by
hiding the control — which is what motivated the read-only decision originally.

**Why a popup and not a fix to the strip:** a popup needs no vertical budget, so
it works at the resolution the strip's tuning never covered. #2128's CSS is
measurement-tuned across #1313/#1336 and is deliberately *not* a prerequisite
here; it stays open.

## Independent review (Fable subagent, isolated worktree)

Run at a different model from the author, and this time with
`isolation: "worktree"` — the #2135 review was not, and its revert-then-restore
experiments corrupted a concurrent full-suite run badly enough that a clean run
was briefly reported as failing. The skill prescribes a worktree for exactly
this; the lesson cost a wrong conclusion once and should not cost it twice.

It ran real code (build, vet, scoped tests, the feature's own e2e, plus ~10
scratch Playwright measurements of its own) and found **one regression I would
have shipped**, plus three real defects.

### 1. BLOCKER — the quick-pay height budget *was* blown, for German, at 1024×600

My comment claimed the row's height was unchanged because this "adds a sibling,
never a third row". True of the row; false of the button. `flex: 0 1 38%` is
158px at 1024px wide — enough for "Open orders", **not** for the German
*"Offene Vorgänge"* (or Spanish *"Pedidos abiertos"*). The label wrapped to two
lines and the row went **51px → 66.6px**, overflowing `.tender` by 8px and
pushing the bottom of the **Card** button — the money-consequential one
ut-docs#1336 was tuned to protect — into that pane's scroll.

Measured by the reviewer; the English baseline clears by only **0.99px**, so
this row never had slack to lend a second line. tr/ar/fa happen to fit, so four
of the five shipped locales would have looked fine.

**This is the sharpest kind of miss: correct in the language I was testing in,
broken in the language the pilot actually runs.** Fixed with
`flex: 0 0 auto; white-space: nowrap`, and pinned by a new e2e test that drives
the label directly rather than trusting a locale to be long enough — verified to
fail at the old value with exactly the reviewer's numbers (`Expected: 51`,
`Received: 66.5625`) and pass with the fix.

### 2. The popup reported a read failure as "No open orders right now"

On a `repo.List` error the fragment returned **200** with the empty-state copy.
That is the one sentence a cashier with three parked orders must never be shown
falsely — it reads as *your order is gone*, and the recovery they would reach
for is re-ringing the whole sale. `/open-orders` renders
`open_orders.error.load_failed` for the identical failure; the popup silently
disagreed. Now returns 500 so the body does not swap and app.js raises the usual
server banner. New test closes the DB and asserts both the non-200 and the
absence of the empty-state copy.

### 3. A refused resume left the popup covering the toast's dismiss button

Refusal behaved correctly (dialog stayed open, `hold.error.busy` shown), but the
reviewer hit-tested the geometry: the dialog sits over the right of the toast,
so from x@50% at 1024×600 — including the **✕** — taps land on the modal. The
cashier could read *"Finish or hold the current sale first"* and not dismiss it.
Worse, that message tells them to act on the sale screen the popup is covering.
Now closes on any answered request, refusal included: getting out of the way
*is* the useful response. New e2e test covers the refusal path, which nothing
tested before.

### 4. Manual and screenshots (already addressed before the review landed)

The reviewer flagged the help topics still telling the cashier to use the strip,
and `guard-docs-shots` failing. Both were fixed in the commit after the one it
reviewed: `open-orders.md` and `sell.md` updated in all five shipped locales
with structure preserved (so `guard-help-drift` stays green with no new baseline
entry), and screenshots regenerated.

### Checked and found sound

No injection path — `jsonVals` output is `json.Marshal`-escaped then
attribute-escaped in a plain-text attribute context (`hx-vals` is not an `on*`
name), the value always starts with `{` so htmx's `js:` eval prefix is
unreachable, and `.Label`/`.TableLabel` land in text nodes. The `listOpenOrders`
extraction is faithful (loop body byte-identical, page error path unchanged).
`.show()` is a no-op when already open; focus returns to the trigger on close;
no #1625/#1628-class duplicate accessible name. RTL verified under `dir=rtl`.
No new locale key, so no language-pack follow-up is implied. Tests confirmed
real by reverting pieces.

### Nits accepted

`open_orders.hint` is now a dead key in four core locales and both packs —
deliberately left: removing a key from `en.json` is the direction
`lang-pack-drift` does not guard, and a dead key costs nothing. `.held-chip-table`
had no CSS rule anywhere (pre-existing, including on the strip); it now appears
in a second context, so it got the quiet chip styling its name implies.
`Esc` does not close a `.show()` dialog — consistent with `#hold-modal`.

## Verification

- Go: `internal/pages` popup tests (contents, ordering, money formatting, empty
  state, resume removes the row, read failure reported).
- e2e, 5 tests: resume from the popup, empty state, refusal closes and explains,
  long label does not grow the row (1024×600), trigger and entry in viewport
  with a ≥46px target at the pilot's 1280×800.
- Full suite green before push.

**Not yet verified on the pilot tablet** — it needs a release. That is the
acceptance criterion still open, and given this card exists because a device
screenshot disagreed with what the server reported, the device check is the one
that counts.

## Second review round — the two CI failures (2026-09-12)

The first round reviewed the feature. CI then failed two tests, both caused
by this card's own change rather than by flakiness, and both fixed here.
Reviewed independently again (different model, isolated worktree).

### What failed and why

1. `tender-panel-reachable.spec.ts` — the RTL (fa) test asserted the
   quick-pay **button** spans the full width of `.tender-default-footer`.
   That held only while the button was the sole child of `.tender-quickpay`;
   this card put the parked-orders trigger beside it, so quick-pay now
   covers part of the row (measured 164.95px short in CI, 141.67px locally).
   The invariant the test exists for belongs to the **row**, so the
   assertion now measures `.tender-quickpay`, and additionally asserts the
   two children genuinely mirror under RTL — a check the old single-child
   shape could not express at all.

2. `parked-orders-popup-2137.spec.ts` — "the popup says so when nothing is
   parked" is a claim about global state. Held orders are DB rows and
   `POST /api/pos/reset` clears only the in-memory basket engine, so
   fixtures.ts's per-file reset does not remove them and any earlier spec
   that parked a sale without resuming it falsifies the claim. It passed
   alone and failed in CI at test ~301 — order-dependent, not flaky.
   Fixed with a `drainParkedOrders` helper.

### Evidence

- Full local `--project=default` run: **443 passed, 0 failed**.
- Negative control: deliberately parking an order reproduces CI's exact
  `element(s) not found` without the drain, and passes with it. (A first
  attempt at this control was invalid — a `-g` filter silently excluded the
  polluting test — and was redone.)

### Review findings acted on

- **Confirmed (fixed):** `/api/pos/resume` refuses a held sale whose payload
  fails `json.Unmarshal` and returns *before* its own `repo.Delete`, so the
  row is never removed. A drain loop that only counts rounds would burn its
  cap and then report "still finding parked orders" — which reads as
  unbounded generation rather than one unresumable row. The reviewer proved
  this live by inserting a malformed payload. The helper now detects the
  same id surviving a resume and fails immediately, naming the id and
  quoting the till's own toast. Re-verified by reproducing that insert:
  fails in seconds with
  `held order hold-corrupt-1 survived a resume … The till answered: Could
  not hold the sale`.
- **Acted on:** the helper was private to one spec. Moved to
  `e2e/tests/helpers.ts` and exported, so the next spec asserting an empty
  held state finds it instead of re-discovering this CI-only failure.
- **Filed, not fixed here:** two tests in this file park orders they never
  resume (the busy-refusal and pilot-resolution cases), so they still leak
  held rows to later spec files. Nothing today asserts a held-order count,
  so nothing breaks — but the systemic gap is only patched at this one call
  site, not closed. Tracked separately rather than widened into this fix.
- **Checked and dismissed:** the RTL assertion is not backwards — DOM order
  (trigger first) and `.tender-quickpay { display: flex }` with no
  `flex-direction`/`direction` override mean the first child legitimately
  renders at the row's right edge under `dir="rtl"`. Reading `.left`/`.right`
  off `getBoundingClientRect` is a measurement API, not authored CSS, so it
  does not conflict with the repo's logical-properties rule.
