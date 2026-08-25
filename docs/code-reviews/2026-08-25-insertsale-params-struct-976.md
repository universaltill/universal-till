# Code review: `InsertSale` positional-argument refactor (ut-docs#976)

**Date:** 2026-08-25
**Card:** universaltill/ut-docs#976 — "universal-till: `data.InsertSale`'s
25-positional-argument signature is one transposed pair from a silent money
bug"
**Branch:** `feat/976-insertsale-params-struct`
**Complexity:** medium (build: Sonnet inline; review: Opus, fresh context,
isolated worktree)

## What shipped

`internal/data.POSRepo.InsertSale` had grown to ~25 positional arguments,
one added per sale-column addition over time. At that arity, two adjacent
same-typed arguments (two money amounts, two basis-point rates) transposed
at a call site compiles cleanly and fails silently — the wrong value lands
in the wrong DB column with no error, only a wrong number downstream on a
receipt or export.

This is a **pure refactor, no behavior change**: introduces
`data.InsertSaleParams`, a named-field struct whose field order is an exact
replay of the old positional-parameter order (deliberately chosen over
mirroring `pos.SaleInput`'s different shape, so the positional→named
translation at every call site stays mechanically auditable). Every call
site updated to build the struct instead of passing positional args:

- `internal/pos/sales.go:403` — the live checkout path (`CompleteSale`).
- `internal/data/pos_repo_service_charge_test.go:36,83`
- `internal/data/pos_repo_batch8_sales_test.go:42,228,257,305,337`

`InsertPayment`'s smaller (13-arg) positional signature was deliberately
left out of scope, per the issue's "if scoped in" — noted in the commit
message as optional follow-up, not silently dropped.

## Independent review

Spawned an Opus subagent, fresh context, isolated git worktree
(`isolation: "worktree"`), briefed with the diff scope, the repository-
pattern/money rules from `CLAUDE.md`, and an explicit instruction to run
the gate itself rather than trust the description.

**Verdict: safe to merge, no blockers.**

The reviewer did the load-bearing check mechanically, not by eye: extracted
the parent-commit versions of all three call-site files, parsed every old
`InsertSale(...)` argument list, zipped each positional index against the
declared `InsertSaleParams` field order, and diffed against the new struct
literals for both value mismatches and unsafe zero-value omissions.

Result across all 8 call sites: **0 problems**. The reviewer additionally
confirmed the `ExecContext` SQL body and its 24 bind arguments are
byte-identical after mechanically rewriting `p.Field` back to the old
parameter names and diffing against the parent commit — the repo method
itself cannot have shifted a column. The highest-risk site (the adjacent
`ServiceCharge`/`ServiceChargeTaxBasisBP` pair — exactly the transposition
class this refactor exists to prevent) was independently confirmed correct.

Commands the reviewer ran itself (not taken on trust): `go build ./...`,
`go vet ./...`, `go test ./internal/data/... ./internal/pos/...` (green),
plus a full `go test ./...` and `gofmt -l .` (clean), and a spot-check of
`guard-data-access.sh`, `guard-kiosk-engine.sh`, `guard-i18n.sh`,
`guard-help-topics.sh` (all pass).

### Findings and disposition

1. **Non-blocking — fixed in follow-up card, not this PR.** The refactor
   trades one silent-failure mode for a smaller one: positional args forced
   every value to be supplied; a struct literal lets a *required* field
   default to its zero value silently (e.g. an omitted `CreatedAt` writes
   empty `created_at`, and this codebase has already established
   — `docs/code-reviews/2026-08-15-sync-journal-currency-createdat-validation-647.md`
   — that SQLite's `NOT NULL` does not catch `""`). Real but small today:
   the sole production caller sets every field explicitly, and the sync/
   journal path is validated upstream of this call. Filed as a Backlog
   follow-up (ut-docs#989) rather than expanding this refactor's diff —
   a required-field guard needs its own small design pass to enumerate
   exactly which fields are genuinely required without breaking the test
   sites that deliberately rely on zero-value omission for optional ones.
2. **Nitpick — fixed.** The `InsertSaleParams` doc comment originally
   claimed its field order mirrors `pos.SaleInput`'s shape; it doesn't
   (different field set/order, `money.Money` vs `int64`). Corrected to say
   what's actually true and why it matters: the order mirrors the *old
   positional signature*, which is what keeps the translation auditable.
3. **Nitpick — no action.** `ServiceChargeTaxBasisBP` is `int`, not `int64`
   per `CLAUDE.md`'s "basis-point rates stay `int64`" rule. Pre-existing
   (matches `pos.SaleInput.ServiceChargeTaxBasisBP` and
   `SaleCharge.TaxBasisBP` already in the codebase) and out of scope for a
   pure refactor that changes no types.

## Verified beyond automated tests

- Full `go test ./...` green (not just the two touched packages).
- All 16 CI-blocking guards from `universal-till/CLAUDE.md`'s "Before
  committing" list run locally, all pass (unaffected by this backend-only,
  non-UI change, but run in full per the standing gate discipline).
- Repository-pattern/money rules confirmed intact: SQL text unchanged and
  still confined to `internal/data`; amounts still `int64` minor units at
  this DB boundary, consistent with the sibling `InsertSaleDiscount`.
- Repo-wide grep for `InsertSale\b` confirms no leftover positional call
  site outside the 8 already converted.

## Safe-to-merge verdict

**Yes.** No blocking findings; one nitpick fixed in this branch, one real
but small non-blocking risk filed as a separate Backlog card
(ut-docs#989) rather than scope-creeping this refactor.
