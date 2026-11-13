# Review: promotion edits record what changed in the audit log (ut-docs#5588)

**What shipped:** `POSRepo.UpdatePromotion` now returns `(PromotionChanges, error)`. Inside the same tx as the UPDATE it reads every editable column once (`type`, `value`, `COALESCE(description/starts_at/ends_at/customer_id,'')`). `promotionDiff` then compares that row with the input and maps each changed column name to `{"from","to"}`. Those are the stored forms: strings, and `value` as an int64 in minor units or basis points. When the unit changes, a `type` entry records it beside `value`. `customer_id` is recorded by id only, never the name, and only when the edit really rewrites it, so a missing field (`KeepCustomer`) or the same id, including a dangling one that is kept, leaves it out. A no-op edit returns an empty map that is not nil. Every error path returns nil. On `POST /api/promotions/{code}/edit`, `promotions_page.go` writes `{"changes": …}` into the existing single `promotion_edit` row. Create, activate and deactivate still store `null`. Two older callers in tests were adjusted only so they compile. No migration, no SQL outside `internal/data`, no new strings, no help change: the Audit page and CSV export print `data_json` raw, and no topic or reference page describes promotion payloads. The `{"from","to"}` shape follows `till_role.go`, `users_page.go` and `sync_users.go`, nested per field.

Sonnet built it. An independent Opus 5.5 subagent reviewed it in a separate worktree, and a Tester pass ran before the review.

## Findings
1. **Low — fixed.** Nothing at page level tested a dated, customer-bound no-op edit. The form sends back a bare end date, and `endsAtInclusive` stores it as `YYYY-MM-DD 23:59:59`. The diff has to compare stored form with stored form, or every re-save would report a false `ends_at` change. The code was already right. Added `TestPromotionsPageEdit_AuditNoOpWithDatesAndCustomer`: create with start and end dates in 2031, a description and a customer, resubmit the same form, expect `{"changes":{}}`. It is genuinely red under a mutation that reads `substr(ends_at,1,10)`, which fails with `ends_at {"from":"2031-03-31","to":"2031-03-31 23:59:59"}`.
2. **Info — accepted.** A legacy or DB-authored row whose `ends_at`, `starts_at` or `description` is stored in a different form (a bare date, a timestamp with `T…Z`, untrimmed text) shows that field as changed on its first re-save through the form. That is true, because the stored value does change. The demo seed and the e2e seed write no dates, so the case is rare.
3. **Info — accepted (non-goal in the design).** The audit insert is still best-effort after commit, outside the repo tx, which is the existing pattern. Moving it into the tx would mean passing the actor into the repo API.
4. **Info — accepted.** The free-text `description` now appears in `data_json` and the audit CSV. A manager writes it and it is not customer data. Customer names stay out, and a test checks that.

No issues in tx handling: the read and the UPDATE share one tx, the diff is built before Commit, and nil comes back on Commit failure. No issues in JSON: the empty map marshals to `{}`, never `null`. No issues in type comparison: both sides are int64, and a type change records both units. Every `UpdatePromotion` caller compiles (`go build ./...`, and `go vet` on both packages).

## Verification
- **TDD re-verified by the reviewer.** Ran `git checkout HEAD~1 -- internal/data/pos_repo.go internal/pages/promotions_page.go`. That alone is a build failure (`assignment mismatch: 2 variables but repo.UpdatePromotion returns 1 value`). Then stubbed it so it compiles: the new signature returns `PromotionChanges{}` around the old body, and the page drops the result. With the stub these were behaviourally red:
  - data: `DiffValueChange`, `DiffCustomerChange`, `DiffKeepCustomerOmitsCustomer`, `DiffTypeChangeRecordsBothUnits`, `DiffDescriptionAndDates`, and `UnknownCodeReturnsNilChanges` (`changes=data.PromotionChanges{} err=promotion not found`).
  - pages: all six `TestPromotionsPageEdit_Audit*` tests, with `payload = null`.
  - `DiffNoOpIsEmptyNonNil` passes against the stub, as it should, since the stub's answer is correct for that case.
  - Restored with `git checkout HEAD -- …`: everything green.
- **Tester.** Ran mutations: drop `customer_id`, swap value from/to, swap customer from/to, always emit `customer_id`, ignore keep, drop `type`, page passes nil. All went red. Dropping the description/starts_at/ends_at entries stayed green, so the Tester added `promotions_edit_diff_5588_tester_test.go`, and each of those mutations is now red. Driven run with the built binary, `UT_AUTH=off`, a temp DB, then `/api/audit/export`:
  - edit value and customer: `{"changes":{"customer_id":{"from":"test-5588-a","to":"test-5588-b"},"value":{"from":100,"to":250}}}`
  - edit type, clear customer, add description: type amount→percent, value 250→1000, customer b→"", description ""→hello
  - edit with customer omitted and nothing else changed: `{"changes":{}}`
  - the create row stays `null`, and no customer names appear.
  - Under `-race`: the promotion tests pass. One unrelated wall-clock test in `internal/data` fails. The full `internal/pages -race` run was not finished.
- **Gates.** `gofmt -l .` clean; `go build ./...`; `go vet ./internal/data ./internal/pages`; `go test ./internal/data -run 'Promotion|Promo'` ok; `go test ./internal/pages -run 'Promotion|Promo|Audit'` ok; `golangci-lint run ./internal/data/... ./internal/pages/...` 0 issues; `guard-data-access.sh`, `guard-fixed-dates.sh` (test dates are 2031) and `guard-i18n.sh` pass.

Pre-flight: review record updated; help topic n/a (the Audit page prints data_json raw, and no topic describes promotion payloads); en.json n/a (no strings); i18n-meta n/a; reference n/a (no reference page lists audit payloads); README n/a; mirrors/ADR n/a (no manifest, host function or taxonomy change).

Verdict: approve. Merge once CI is green.

Deferred: none. Payloads for promotion_create, activate and deactivate, and an in-tx audit insert, are non-goals in the design and only become a card if the owner asks.
