# Code review: cash adjustment / Pfandrückgabe bypassed the German TSE hard gate (ut-docs#998)

**Branch:** `fix/998-cash-adjustment-fiscal-gate` · **PR:** universal-till#601
**Reviewer:** independent Opus subagent, isolated worktree, fresh context ·
**Author:** Sonnet (this pipeline cycle)

## What shipped

ADR-0048's German TSE hard gate was swept onto every `pos.CompleteSale`
call site by ut-docs#731. `internal/pages/shifts_api.go` has two handlers
that take cash physically out of the drawer without ever calling
`CompleteSale`, so that sweep never reached them — the gap #731's own
review record filed as ut-docs#998:

1. `RecordCashAdjustment` (`POST /api/shifts/adjustment`) — a payout, a
   skim, or a negative "adjustment".
2. `PfandRueckgabe` (`POST /api/shifts/pfandrueckgabe`) — a bottle-deposit
   cash payout.

The product owner's decision was "gate it like a sale — reuse the same
`enforceFiscalGate` helper, same operator message, same manager-override
path", plus a guard so a future money-moving completion path without the
gate fails loudly instead of relying on a reviewer noticing.

Fix: a new `enforceCashAdjustmentFiscalGate` wrapper in `shifts_api.go`
calls the shared `enforceFiscalGate` and maps its three outcomes onto the
already-shipped localized copy (`refund.error.fiscal_tse_failing` /
`refund.error.fiscal_never_configured` → 409;
settings-store read failure → 500 `refund.error.server`) — the exact
three-way `errors.As` shape #731's review landed on `inventory_api.go` as
its S1/S2 fix. No new locale keys. A companion
`auditCashAdjustmentOverride` writes the per-completion
`unsigned_override` audit marker on `AllowedWithOverride`, keyed
`entity_type="shift_adjustment"` / `entity_id=<adjustmentID>` — deliberately
distinct from `pos.RecordCashAdjustment`'s own `entity_type="shift"` /
`action="cash_adjustment"` row, so the marker identifies the one payout
taken unsigned rather than the shift as a whole.

Gate placement: in `RecordCashAdjustment`, scoped to `req.Amount < 0` and
checked *after* the manager-PIN gate and *before* `pos.RecordCashAdjustment`;
in `PfandRueckgabe`, unconditional (the endpoint validates `Amount > 0` then
negates, so it is always a payout).

Tests: `internal/pages/shifts_cash_adjustment_fiscal_gate_test.go`
(8 tests, mirroring `refund_fiscal_gate_test.go` /
`inventory_return_fiscal_gate_test.go`) plus
`internal/pages/fiscal_gate_coverage_test.go`, the source-scanning
regression guard.

## Independent review — verdict on first pass: NOT safe to merge; fixed, now safe

Full independent pass (different model, fresh context, isolated worktree).

**Verified correct and left unchanged:** gate ordering on both handlers (no
early return, no `req.Type` value, and no form-vs-JSON decode path reaches
`pos.RecordCashAdjustment` while skipping the gate — `type` is validated to
`payout|adjustment|skim` and `payout`/`skim` are both forced negative
above, so every cash-leaving request funnels through the `Amount < 0`
branch); the `fiscal.Gate` zero value (`Allowed`, iota 0) so the ungated
positive-adjustment path cannot write a spurious `unsigned_override` row;
money handling (`req.Amount` is a raw `int64` DTO field compared at the
boundary, converted once via `money.FromMinor` — no `Money`/`int64`
mixing); i18n (reuses three existing keys, no new user-facing literal;
`guard-i18n.sh` green); repository pattern (no SQL added outside
`internal/data`; `guard-data-access.sh` green); `{data,error}` envelope;
kiosk isolation; ADR-0040 compliance wording. Neither of this pipeline's
two recurring bug classes applies — the diff writes no files, so there is
no missing `os.MkdirAll` and no cwd-relative path that wanted `paths.Data`.
The new tests' cwd-relative `web/locales/fa.json` read is safe and matches
precedent: `newShiftsAPITestDeps` calls `chdirRoot(t)`, exactly as
`newRefundFiscalTestDeps`/`newInventoryFiscalTestDeps` do.

**Blocker found and fixed — B1: the user manual was left contradicting the
product.** `web/help/*/sell.md` § "German shops: TSE and real sales" states
the gate's scope explicitly and exclusively — "before completing each sale
**or refund**", "the sale or refund is refused", "sales and refunds are
paused". After this change a German system-of-record shop also has cash
payouts refused, on a screen the manual never mentions. A shop owner whose
Pfandrückgabe is refused would find the manual actively telling them that
can't happen. `universal-till/CLAUDE.md`'s standing product-owner
instruction (2026-08-06, ut-docs#324) is unambiguous: the manual ships in
the same branch, not after. This is also the precise thing #731 got right
for its own scope — it broadened the same three sentences across all four
locales when it added refunds. Fixed: the section now reads "each sale,
**refund, or cash payout**" with a sentence defining what counts (a payout
or a cash-removing adjustment on the Shifts page, and a Pfandrückgabe;
adding cash is unaffected), in `en`/`tr`/`fa`/`ar`. Deliberately *not*
broadened: the "a banner stays on the sale and refund screens" clause —
that remains literally true, since the override banner is not rendered on
the Shifts page (see D2). `web/help/*/reports.md` § "Cash adjustments &
payouts (Shifts)" — the topic that documents the actual form — gained a
paragraph pointing at the check and at `sell.md` for the override, also in
all four locales. `make docs-shots` regenerated (text-only change; the
three unrelated PNG diffs are the known screenshot nondeterminism).

**Blocker found and fixed — B2: the new regression guard did not guard the
likeliest regression.** `fiscal_gate_coverage_test.go` matched
`pos.RecordCashAdjustment(` against raw file text and then asked whether
*the same file* mentions a gate helper anywhere. Both gated handlers live
in `shifts_api.go`, so a third payout handler added to that same file — by
far the most likely way this regresses — would forget the gate and the
guard would stay green. **Confirmed empirically, not argued:** deleting
`PfandRueckgabe`'s gate entirely left
`TestFiscalGate_EveryRecordCashAdjustmentCallSiteIsGated` passing. The
text-matching also carried a false-positive risk in the other direction (a
doc comment or string literal containing `pos.RecordCashAdjustment(` would
have tripped it, pushing the next author toward the escape hatch for no
reason). Fixed: rewritten over `go/ast` — each call site is resolved to its
enclosing `FuncDecl` and the gate must be called *in that same function*;
a call outside any function body is reported separately rather than
silently skipped. The `// fiscal-gate:exempt <reason>` line hatch and the
`checked == 0` "this guard has nothing to check" failure are both kept
(the latter is correct and load-bearing: it is what stops a rename from
turning the guard into a no-op). Failures now name file, line and function
and use `t.Errorf`, so several offenders report in one run.

**Should-fix, found and fixed — S1:** `internal/fiscal/fiscal.go`'s package
doc enumerates "every money-moving completion path" behind
`enforceFiscalGate` and still listed exactly three. #731's review had
fixed this same staleness as its own S5; leaving it stale again would make
the next sweep repeat #998's mistake for a fourth time. Now lists all five,
notes that the two new ones never call `pos.CompleteSale` (which is *why*
the previous sweep missed them), points at the coverage guard, and records
the `CloseShift` skim gap below as a known non-gated path.

**Accepted as-is — A1: the `req.Amount < 0` scoping.** A positive
"adjustment" (an *Einlage* / float top-up) is not gated. Under DSFinV-K a
cash deposit is arguably as recordable as a withdrawal, so a strict reading
of ADR-0048's purpose could cover it. Accepted here because: the product
owner's decision text scopes the card entirely to cash *leaving* the till;
the implementation exactly mirrors the manager-PIN gate immediately above
it in the same function, which is sign-scoped for the same reason
(ut-docs#266); and widening it would refuse float top-ups on a German shop
with a failing TSE, which is a behaviour change with real operational cost
and is not a reviewer's call to make unilaterally. Flagged for the product
owner as D1 rather than silently decided.

## Commands run (this checkout, post-fix)

- `gofmt -l .` — clean. `go build ./...` — clean. `go vet ./...` — clean.
- `go test ./internal/pages/... -run 'TestFiscalGate|TestRecordCashAdjustment|TestPfandRueckgabe'` — pass.
- `go test ./... -count=1` (whole module, not just `internal/pages`) — pass,
  no failures in any sibling package.
- All 29 CI-blocking guards in `.github/workflows/ci.yml`'s `build` job,
  run individually — all pass, including `guard-data-access.sh`,
  `guard-i18n.sh`, `guard-compliance-claims.sh`, `guard-help-topics.sh`
  and `guard-docs-shots.sh` (the last after `make docs-shots`).

## TDD / regression re-verification (performed personally, not taken on trust)

1. **`RecordCashAdjustment` gate disabled** (condition forced false):
   4 of the 6 adjustment tests failed —
   `…PayoutBlockedWhenTSENeverConfigured` and
   `…PayoutFailingTSEBlockedWithoutOverride` on *status* (200 +
   `success:true` instead of 409), `…PayoutOverrideUnblocksAndAudits` on
   its pre-override refusal, and `…RefusalIsTranslated` on the missing
   `fa` copy. The two deliberate regression pins
   (`…PositiveAmountUnaffected`, `…NonGermanShopUnaffected`) correctly
   stayed green. Restored → green.
2. **`PfandRueckgabe` gate deleted** (replaced by a bare `var gate
   fiscal.Gate`): both Pfand tests failed. Worth recording *how*: with a
   partial bypass that still wrote the 409 header before continuing,
   `…BlockedWhenTSENeverConfigured` failed on its **row-count** assertion
   ("expected no cash_adjustment audit row, got 1") rather than on status
   — so that assertion is genuinely load-bearing, not decoration. With the
   gate removed outright both failed on status. Restored → green.
3. **Coverage guard, before B2's fix:** deleting `PfandRueckgabe`'s gate
   left the guard **passing** — the finding above, reproduced rather than
   theorised.
4. **Coverage guard, after B2's fix:** the same deletion now fails with
   `shifts_api.go:623:24: func PfandRueckgabe: pos.RecordCashAdjustment is
   called with no ADR-0048 fiscal gate in the same function …`.
5. **Throwaway ungated call site** — a temporary non-test file in
   `internal/pages` calling `pos.RecordCashAdjustment` with no gate: guard
   fails naming that file, line and function. The same file also carried a
   string constant containing the literal text `pos.RecordCashAdjustment(`
   to prove the AST matcher ignores non-code occurrences — it did (one
   finding, not two). Adding `// fiscal-gate:exempt throwaway probe` on the
   call line turned it green, confirming the hatch works. File deleted;
   `git status` clean, guard green.

## Deferred — real, out of scope for this card

- **D1 — positive cash adjustments (Einlagen) are not gated.** See A1
  above. Needs a product/compliance decision, not a reviewer's; file as a
  follow-up card alongside D2 if the pilot's tax advisor wants Einlagen
  covered.
- **D2 — the TSE override banner never reaches the Shifts page.** The
  `fiscal-override-banner` component renders on `index.html` and
  `refund.html` only. A cashier taking a payout during an active override
  now has it flagged unsigned in the journal with no on-screen warning —
  structurally the same gap ut-docs#1001 fixed for the refund screen. The
  `fiscal.banner.override_active` copy ("sales and refunds are being
  recorded without a TSE signature") is correspondingly incomplete. Left
  alone deliberately: adding a banner to a new surface is a UX change
  beyond this card, and broadening the copy without the banner appearing
  where payouts happen would not help anyone.
- **D3 — the skim recorded at shift close is not gated.** `pos.CloseShift`
  writes its own `cash_adjustment` audit row inside the close transaction
  rather than calling `pos.RecordCashAdjustment`, so it is a third
  cash-out-of-the-drawer completion that neither this gate nor the new
  coverage guard reaches. Genuinely the same defect class this card exists
  to close, and found by the same reading. Not fixed here on purpose:
  blocking a *shift close* has a much worse failure mode than blocking a
  payout — an operator could be unable to close the till at end of day —
  so whether and how to gate it is a product-owner decision. Recorded in
  `internal/fiscal`'s package doc so the next sweep sees it.

## Safe-to-merge verdict

**Yes**, after the fixes above (B1, B2, S1 resolved and re-verified; A1
accepted with rationale; D1–D3 deferred and documented). No remaining
blockers or should-fix items.
