# Code review — cloud-master bootstrap over the till's seed rows (ut-docs#4494)

- **Date:** 2026-10-16
- **Card:** ut-docs#4494 (task of epic ut-docs#4453, ADR-0162 §4 "Bootstrap")
- **Author:** Opus 5.5 (Dev subagent). **Reviewer:** Fable (independent, different model)
- **Branch:** `feat/4494-cloud-bootstrap-seed`

## What shipped

ADR-0162 §4 says the setup wizard skips its seed catalogue on a
cloud-master store. A till is paired with a my. shop only after its wizard
finishes, so the bootstrap pull (`internal/catalogpull`) handles the seed
rows instead:

- **Seed tax codes are adopted.** Case: an unlinked cloud `tax_code` row
  whose trimmed name equals an unlinked migration-seed tax code
  (`data.SeedTaxCodeIDs`: `tax_std`, `tax_red`, `tax_zero`). The row is
  applied onto that seed row and linked. Before, it was refused for a
  UNIQUE name clash, and every item naming it was refused too. Its audit
  row adds `adopted_seed: true`. A non-seed local row of the same name
  stays refused (general adoption: ut-docs#4465). So does a seed row
  already linked to another cloud row.
  The SQL lives in `CloudLinkRepo.AdoptableSeedTaxCode`. It uses BINARY
  collation, the same as the UNIQUE constraint.
- **Seeded gift voucher item retired.** After a run that applied rows,
  `Run` calls `POSRepo.RetireSeededGiftVoucherItem` (ut-docs#4399's rules),
  outside every chunk transaction. The live-basket hook is asked first.
  The call is best-effort: a failure is only logged.
- The wizard is unchanged; its code now has a comment pointing to the pull.
- `docs/data-model.md`: a seed-rows paragraph. ut-docs carries the
  ADR-0162 §4 clarification and the feed contract note.
- e2e `cloud-catalog-bootstrap-4494.spec.ts` (`diagnostics` project). An
  in-process fake ut-cloud answers the check-in with
  `catalog_master: "cloud"` and serves "Standard VAT" 19 %, a category and
  an item. The item appears in the till's catalogue, and Tax codes shows
  exactly one "Standard VAT" at 19 %.

## TDD, re-verified independently

The reviewer reverted `apply.go`, `catalogpull.go` and `cloud_links_repo.go`
to the base in a separate worktree and kept the new tests:
- These 4 tests fail: adopt (Refused:2, Applied:0), already-linked seed,
  voucher retired, voucher retried after a basket hold.
- These 2 negative tests pass, as they should: non-seed same name, and
  edited voucher kept.

After restoring, all 6 pass. The Dev also ran the e2e spec against the base
code: it fails, because the item never appears. On the fix it passes.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | medium | The basket check and the retire delete are not atomic. A cashier could tap the seeded voucher tile in that millisecond window. | **Accepted.** It is the same pattern the import path and the `delete_item` directive already use, and the window opens once per till (the marker is cleared). |
| 2 | low | Adoption matches the seed row's *current* name. A seed code the shop renamed is adopted when the cloud sends that new name. | **Accepted.** That is the intended effect, since there is no clash either way. The ADR wording says "matches an unlinked migration-seed tax code". |
| 3 | low | Suppose the cloud deletes "Standard VAT" and recreates it under a new id, and both happen before a `410` resync. During the full pull the old link still holds `tax_std`, so the new row is refused until its version moves. | **Accepted, known limitation.** It is the general sticky-refusal behaviour (ut-docs#4465). An ordinary feed (tombstone, then create) works. |
| 4 | low | `res.Changed = true` after the retire was dead code: `Applied > 0` already sets it. | **Fixed:** the line is removed, and a comment says why. |
| 5 | low | The prose said "once the pull brought the shop's own". The repo rule is "another active voucher item exists". | **Fixed** in the doc comment. `data-model.md` already stated the rule. |
| 6–7 | nit | The `entity` parameter is always tax_code, and there is one extra SELECT per run. | Left as is. |

The reviewer also checked these and found them OK:
- A refused adopted row (country rate rule) leaves `tax_std` untouched and
  still adoptable.
- A tombstone or prune deactivates the code and never deletes it, so FKs
  and history are safe.
- Offline-first: nothing runs on the sale path.
- SQL stays in `internal/data`, and no migration is touched.
- There are no file writes.
- The e2e spec cannot disturb the other diagnostics specs: the next
  check-in resets `catalog_master` to `till`.

## Gate

- Commands: `gofmt -l .` (empty), `go build ./...`,
  `go vet ./internal/catalogpull/ ./internal/data/`,
  `go test -race ./internal/catalogpull/ ./internal/data/`,
  `golangci-lint run ./...` (0 issues).
- Guards: data-access, core-neutral, deadcode-baseline-growth, i18n,
  netaccess, mail-addresses, card-data-schema, kiosk-engine,
  compliance-claims, competitor-naming, e2e guards.
- e2e: `npx playwright test --project=diagnostics` gave 8/8 passed.
- Full `go test ./...`: every package passes except `internal/plugins`, which
  hit the 10-minute timeout in its own HTTP-egress tests in this sandbox.
  That package is not touched by this diff, and CI decides.
- Not run locally: the full deadcode check. The local `deadcode` binary is
  built with go1.26, and the module needs go1.27. CI runs it.

## Verdict

Safe to merge.
