# Code review: LAN sync journal payload — snake_case JSON tags

**Date:** 2026-08-13
**Branch:** `feat/262-lan-sync-json-snake-case`
**Closes:** universaltill/ut-docs#262 ("LAN sync journal payload (data.SaleDetail) has no json tags — wire keys are PascalCase, not snake_case")
**Reviewer:** independent subagent, model override `opus` (different from the implementing session's model), fresh context, isolated worktree

## What shipped

`internal/data/pos_repo.go`'s `SaleDetail`, `SaleDetailLine`, `SaleDetailPayment` had zero `json` struct tags, so the LAN sync journal payload (`internal/pages/sync_sales.go`'s `journalSale`, `POST /api/sync/sales`, replica → primary) marshaled them PascalCase — violating `universal-till/CLAUDE.md`'s snake_case rule. All three structs now carry snake_case tags, scoped correctly: only `sync_sales.go` marshals `SaleDetail` externally, confirmed by grep against the other five internal-only consumers (`refund_page.go`, `print_api.go`, `invoice_page.go`, `kitchen_print.go`, `journal_page.go`).

`applyJournal` also now rejects a journal entry missing `id`/`receipt_no`/`sale_type` before applying it (`missingJournalFields`), with the specific field(s) named in the error and the sale id included for correlation.

## Independent review

Spawned a `general-purpose` subagent, model `opus`, isolated `git worktree` (branched from a `WIP: pre-review snapshot` commit on this feature branch so the revert-then-restore TDD re-verification below never touched the orchestrator's shared checkout — ut-docs#386's mitigation). Briefed with the exact diff, the compatibility hazard's stated justification, and told to actually run the gate and verify the justification itself, not just read it.

**Real findings, all fixed before merge:**

- **Blocking (ADR-0039) — unversioned breaking change to a live wire contract, with a proven non-self-healing corruption path in the reverse direction.** The original guard only protected *new primary receiving an old-format payload*. Traced through `pos.CompleteSale`: a **new-format replica pushing to an old, unpatched primary** decodes `receipt_no`/`sale_type`/etc. as empty on the old binary (which has no guard at all), silently mints a new local receipt number, and can book the sale at zero value — permanently, since the sale id is now consumed and the eventual-correct record becomes `SaleExists → true` → skipped forever. ADR-0039 ("every integrated component publishes a versioned service contract... a breaking change with no version bump is a defect... review should treat an unversioned breaking edit as blocking") directly covers this: `reference/service-contracts.md` already lists "Cloud sync / multi-till replication (ADR-0011)" as a known interface needing a contract and not having one. **Fixed:** wrote `ut-docs/reference/contracts/pos-lan-sync-journal.md` (v1.0.0, experimental) documenting the `POST /api/sync/sales` interface in full, with an explicit Guarantees-section compatibility rule — **upgrade the primary before any replica**, since only the primary side can currently detect a version-skewed peer. Cross-referenced from `ADR-0011` (`§2` amendment note). Version-negotiation itself (a protocol version field, or dual-format decoding) is recorded as a follow-up, not built here — `VERSION?=0.1.0`, no git tags, no CHANGELOG, so no multi-till installs plausibly exist in the field yet; the documented ordering constraint is the correct-sized fix for where the product actually is.
- **Should-fix — the guard's own justification comment was half wrong, and duplicated in two places.** Go's `Unmarshal` matches a tag case-insensitively, so `"ID"` *does* still match tag `"id"` (only case differs) — `Sale.ID` actually survives a stale-peer decode fine. It's `ReceiptNo`/`"receipt_no"` and `SaleType`/`"sale_type"` that go empty (the underscore makes them different strings, not just different case) — those are the load-bearing checks, not `ID`. Left uncorrected, a future maintainer trusting the comment could reduce the guard to `ID == ""` alone and silently disable it. Fixed: corrected both comments (`sync_sales.go`, the test file) to state the accurate mechanism.
- **Should-fix — the rejection wasn't actually diagnosable.** The handler logs/returns `j.Sale.ReceiptNo` — precisely the field that's empty in the case this guard exists to catch, so the operator saw `sync apply  from Replica 1: ...` with no identifier. Fixed: added `missingJournalFields`, which names exactly which required field(s) are missing, and the error now includes `Sale.ID` (which does survive) for correlation: `invalid journal entry (sale id %q): missing %s`.
- **Should-fix — the new test skipped its strongest assertion exactly where it mattered most.** `TestApplyJournal_RejectsMissingRequiredFields`'s "no sale written" check was gated on `tc.j.Sale.ID != ""`, so the **missing-id** case never verified anything — the worst pre-fix case, since an empty `SaleID` makes `pos.CompleteSale` mint a *new* random id and write a real row that `SaleExists("")` could never see. Fixed: the test now asserts `SELECT COUNT(*) FROM sales` before/after for every subtest, not a per-id existence check. Re-verified by temporarily dropping the `id` check out of `missingJournalFields` in isolation (not the full guard) — the missing-id subtest genuinely fails (`applyJournal` returns success, ~5s runtime consistent with a real `CompleteSale` write), confirming the count-based assertion is the one that actually catches this case, then restored.

**Judged out of scope, not fixed — logged for follow-up:** `Currency`, `CreatedAt`, `CashierID`, and per-line `ItemID`/`UnitPrice` stay unvalidated by this guard. Every genuine version-skew payload is already caught via `receipt_no`/`sale_type`, and `pos.CompleteSale` has partial downstream defenses (rejects zero lines, defaults `SaleType`/`Currency`, rejects underpayment) — but it does accept `UnitPrice=0`/empty `ItemID` from an authenticated peer, which `CLAUDE.md`'s "validate all external input (…devices)" arguably still wants closed. Filed as ut-docs#647 (see close-out).

## TDD, independently re-verified twice

**Reviewer's own pass:** restored `HEAD~1` (pre-fix) versions of `pos_repo.go`/`sync_sales.go` inside the isolated worktree, re-ran the two new tests — both failed with the exact claimed symptom (wire payload PascalCase throughout; all three `RejectsMissingRequiredFields` subtests failed at the "expected an error" assertion); restored the fix, both passed; confirmed the worktree was byte-identical to the branch afterward.

**Post-fix-round re-verification (this session, after applying the review's findings):** re-ran the same revert→run→restore sequence for the corrected guard, and additionally isolated the `id`-check specifically (removed only that one check from `missingJournalFields`, left `receipt_no`/`sale_type` guarded) to confirm the missing-id subtest's new COUNT-based assertion is the one doing the real work, not a leftover pass from the id-check. Confirmed, then fully restored and re-verified green.

## Full gate (final, post-fix)

Reviewer's pass (isolated worktree): `go build ./...`, `go vet ./...` — clean. `gofmt -l` on the 3 touched files — clean. `go test ./internal/data/... -race` — `ok, 439.6s`. `go test ./internal/pages/ -race -timeout 30m` — `ok, 599.8s` (the default combined-run 10-minute package timeout was hit once, traced to a slow, unrelated, pre-existing test — `TestAskTaxRateBP_OverflowAndConcurrency`, ~4097 sequential DB+event-bus round-trips under `-race` sitting right at the 600s default — not a regression from this diff, no `DATA RACE` reported; logged as ut-docs#648, a CI flake-hazard follow-up, not fixed here). `guard-data-access.sh`, `guard-i18n.sh`, `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`, `guard-help-topics.sh` — all pass.

This session's pass (after the review-round fixes, on the restored/updated branch): `go build ./...`, `go vet ./...`, `gofmt -l` — clean. `go test ./internal/pages/... -run 'Sync|Journal' -v` — all 62 tests pass, including the two new/corrected ones.

## Verified beyond automated tests

- Confirmed via grep: no other struct on this wire surface (`journalSale` and everything it embeds) still lacks tags; no code anywhere reads `SaleDetail`/`SaleDetailLine`/`SaleDetailPayment` via reflection on struct tags (`saleIsTaxInclusive` and friends read Go field names directly — unaffected by the tag change).
- Confirmed backend-only: `git diff` against `web/`, `README.md`, `docs/` (excluding this review record) is empty; i18n and help-topic guards pass; no user-facing strings touched — no manual/README update owed.
- Test data generic throughout ("Apple", "ABC", "cash", "GBP", "Replica 1", "Task Runner"-style placeholders) — no real client/shop name. No secrets/credentials introduced or exposed.

## Verdict

**Safe to merge.** The one blocking finding (unversioned breaking wire change, ADR-0039) is resolved by the new published contract and the documented upgrade-ordering rule, not by building version negotiation — judged the correctly-scoped fix given `VERSION 0.1.0`/no installs in the field. All should-fix findings applied and independently re-verified. One item deliberately deferred as a new Backlog card (ut-docs#647); one unrelated pre-existing flake-hazard logged as a new Backlog card (ut-docs#648), not fixed here.
