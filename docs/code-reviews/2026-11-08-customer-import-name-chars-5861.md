# Review: customer import holds names to the single-line name rule (ut-docs#5861)

Date: 2026-11-08. Lane: `lane:cloud-41`. Complexity: easy.
Built by the orchestrator (Opus 5.5) as a one-file fix `[no-subagent: one-line predicate swap + its test]`;
reviewed by an independent Sonnet subagent; tested by a separate Sonnet Tester subagent.

## What shipped
- `internal/customerimport/apply.go` `invalidFields`: the name uses `data.CheckSingleLineName`
  (ut-docs#5858) instead of `hasControl`, so `\n`, tab, C1 (U+0085), U+2028/2029, bidi overrides,
  zero-width/soft hyphen and unbalanced isolates come back `invalid_fields` [`name`] per row.
  Email, phone, address, postcode and external id keep `hasControl` (address stays multi-line).
- `TestApply_NameRefusesLineBreaksAndInvisibleFormatting`: 8 refused names (one via `force_fields`),
  an RTL name with RLM created and stored byte for byte, a multi-line address accepted.
- `docs/data-model.md` customer import paragraph; ut-docs `reference/contracts/cloud-import-jobs.md`
  "Kind customers" note (branch `docs/5861-customer-name-single-line`).

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| F1 | medium | my. customers preview does not apply the rule, so such a row passes my. and comes back `invalid_fields` after sending (job `partial`) | accepted (rows report per row, as the card asks); contract note added; my. pre-send check added to #5860 (comment) |
| F2 | low | raw invisible characters in the test source | fixed: `\uXXXX` escapes |
| F3 | low | accepted rows only asserted `!= Invalid`; RTL name not checked byte for byte | fixed: `Created` + stored-name query |
| F4 | nit | no `force_fields` row with a bad name | fixed: soft-hyphen row with `ForceFields: [name]` |
| F5 | nit | `hasControl` still allows `\n`/`\t` in other fields | accepted: by design (card) |
| F6 | nit | data-model sentence could mention trimming | accepted: `CheckSingleLineName` doc says so |

Tester: red without the fix (all refused rows created, `customers = 7, want 2`; re-run by the orchestrator
after F2–F4: 8 failures), green with it; `internal/customerimport` and the `internal/pages` ImportCustomers
tests pass with `-race`; invalid name is a per-row result, not a chunk failure. Untested, accepted: a
leading/trailing line break is trimmed before the check (same as the admin screens). Backend only, no UI
touched — no driven run. Unrelated under `-race` in this container: `internal/data`
`TestStockUploadPendingAndHasSendable_FastWithRandomCloudIDsAndUnlinkedMarks` (timing bound) and an
`internal/pages` package timeout in `TestCloudKioskUnlock_PlatformRefusalsMapToReasons` (passes alone).

Pre-flight: help topic n/a (no screen or wording change; import summary already lists invalid rows);
reference updated (contract note); data-model updated; en.json n/a; README n/a; follow-up → #5860.

## Gate
gofmt clean; `go build ./...` ok; golangci-lint 0 issues; `go test ./internal/customerimport/ -race` ok;
`ci.yml` guards all pass locally except `guard-deadcode-baseline.sh` (container's deadcode built with
go1.26 vs module go1.27) and `guard-shellcheck-version.sh` (no shellcheck here) — tooling, left to CI.

Verdict: merge.
