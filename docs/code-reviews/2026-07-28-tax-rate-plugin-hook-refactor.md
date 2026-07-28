# 2026-07-28 — Move dine-in/takeaway tax rules out of core, into a plugin hook

## Context
Farshid's question, directly: "why everytime I ask something like changing
the tax for germany... you change a lot in the pos core. I thought the core
things doesn't need to change a lot and everything can be change by
plugins." Fair — this session's earlier PR (`2026-07-28-order-type-tax-
switching.md`) embedded Germany's specific §12 UStG dine-in/takeaway rule
directly in core: a new `tax_codes.takeaway_rate_basis_points` column, a
`Config.ReducedTaxRateBasisPoints` global setting, and the actual switching
logic (`effectiveTaxRateBP`), plus a DE=7% seed hardcoded into the setup
wizard. The UK has its own version of the same problem (hot takeaway food
VAT), and every other country's quirk would have needed its own core PR
under that design — exactly the pattern being pushed back on.

## What blocked doing this the "obvious" way from the start
There was no way for a plugin to answer core with a *computed value*. The
only existing blocking hook (payment authorization) only supports
accept/reject — `EventHandler` was `func(ctx, event) error`, and
`WasmRuntime.HandleEvent` captured a WASM plugin's stdout but only logged
it, never returned it to the caller. Building "ask a plugin to compute
something" required extending the plugin runtime first.

## What changed

**Plugin runtime (`internal/plugins`)** — generic, reusable, not tax-specific:
- `EventHandler` now returns `(json.RawMessage, error)` instead of just
  `error`. `Publish`'s Blocking case discards the value (unchanged external
  behavior — payment authorization still only cares about the error).
- `WasmRuntime.HandleEvent` returns the plugin's trimmed stdout as the
  response instead of only logging it.
- New `EventBus.Ask(ctx, eventType, payload) (json.RawMessage, bool, error)`:
  blocking, returns the first answering subscriber's response, `ok=false`
  when nobody answers. Events ending in `.ask` auto-run Blocking, same
  convention as the existing `.authorize` suffix rule.
- This is genuinely reusable beyond tax — any future "ask an installed
  plugin to compute X" hook (a pricing-rule plugin, say) can use it as-is.

**Core (`internal/pos`)** — reverted to knowing nothing about any country's
tax rules:
- Removed: `BasketLine.TakeawayRateBP`, `Config.ReducedTaxRateBasisPoints`,
  the pinned-vs-global-fallback branching logic, the DE=7% setup-wizard
  seed, `common.RuntimeState.ReducedTaxRatePct` and its settings-key
  plumbing (5 files: `common/deps.go`, `common/state.go`, `init.go`,
  `settings_page.go`, `setup_page.go`) and the wizard's `reducedTax` Alpine
  state (`web/ui/pages/setup.html`).
- Added instead: `TaxRateAsker` interface (`AskTaxRateBP(l, orderType) (bp,
  ok)`), `Service.SetTaxRateAsker`/`taxAsker` field. `effectiveTaxRateBP`
  asks the installed asker first; `ok=false` (or no asker at all — the
  default) falls back to the line's own configured rate, exactly today's
  plain pre-Germany behavior.
- `BasketLine.TaxCodeID` (new, replacing `TakeawayRateBP`): which tax code
  a line's rate came from, so a plugin can tell item categories apart
  without core interpreting what any of them mean. Threaded through the
  same 5 SQL queries in `internal/data/pos_repo.go` that the previous PR
  touched (`i.tax_code_id`, no JOIN needed — simpler than the column it
  replaces).
- `OrderType`/`OrderTypeTakeaway` stayed in core: dine-in vs. takeaway is a
  genuinely universal POS concept (kitchen tickets already had it;
  SumUp's own research confirms the same pattern), not a German invention.
  What it does to *tax*, specifically, is now 100% a plugin's call.

**Wiring (`internal/pages/tax_hook.go`, new)**: `pluginTaxRateAsker`
implements `TaxRateAsker` by calling `EventBus.Ask("tax.rate.ask", …)` with
the line's item/tax-code/rate and the sale's order type; installed on
`d.Engine` once at boot (`init.go`). `internal/pos` still never imports
the plugin subsystem — this file is the seam, matching how
`blockingPaymentEvent` (`internal/pages/refund_page.go`) is the seam for
payment authorization.

## Judgment calls
- **`TaxCodeID`, not the tax code's own rate data.** Passing an opaque ID
  keeps core from needing to know or store anything about what a "takeaway
  override" even is — a plugin can maintain its own mapping (tax code →
  override rate) in its own settings/storage, entirely outside core's
  schema. (Follow-up, not built here: that mapping currently has no admin
  UI in the plugin either — same pre-existing gap flagged in the prior
  PR's review, now on the plugin side instead of core's.)
- **First answerer wins, not "ask everyone."** Mirrors how payment methods
  are matched 1:1 by entry key — a tax plugin is expected to be
  authoritative for lines it recognizes, not one of several competing
  opinions.
- **`.ask` suffix auto-blocks**, reusing the existing `.authorize` pattern
  rather than inventing a new registration mechanism plugins would need to
  opt into explicitly.

## Verification
`go build ./...`, `go vet ./...`, `go test ./...`, `gofmt -l`, both CI
guard scripts all pass. New tests: `TestEventBus_Ask` (the generic
mechanism: no subscriber, a subscriber that declines, one that answers, one
that errors), `TestOrderTypeTaxSwitching` (rewritten — proves core is inert
with no asker installed, then exercises a fake asker the same shape a real
tax plugin would provide), `TestPriceResolverAdapter_TaxCodeID` (SQL-level,
replacing the old TakeawayRateBP test).

## Not done here
`ut-plugin-tax-de` doesn't answer this hook yet — that's the next, separate
change (different repo). Core's side is complete and testable independent
of whether any plugin actually subscribes.
