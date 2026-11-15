# Review: /sync-quarantine reason key for a single-purpose voucher top-up (ut-docs#5134)

- **Date:** 2026-10-28
- **Card:** universaltill/ut-docs#5134 (follow-up to #4416, review finding 4)
- **Branch:** `fix/5134-single-purpose-topup-quarantine-key`
- **Author:** Sonnet 5.5 (Dev subagent); wording revision by Opus 5.5 (orchestrator)
- **Reviewer:** Fable 5.1, independent fresh context (reviews both the Sonnet code and the Opus wording)

## Change

`permanentJournalFailureReason` can return "single-purpose voucher on top-up
replay" since #4416. `quarantineReasonKeys` had no key for it, so
/sync-quarantine showed the raw English reason. Now:

- New key `sync.quarantine_reason.single_purpose_voucher_top_up` in en/ar/fa/tr,
  with `en.meta.json` metadata. en: "Top-up of a voucher for a specific item
  refused on replay". "Voucher for a specific item" is the term the till
  already uses for a single-purpose voucher in every locale
  (`pos.toast.voucher_single_purpose_tender`, `vouchers_import.status.single_purpose`).
- `quarantineReasonKeys` maps the reason, with its own comment.
- `TestPermanentJournalFailureReason_AllReasonsHaveALocaleKey` gains the
  `ErrVoucherTopUpRefused` + `ErrVoucherNotMultiPurpose` shape.
- The #4416 review record notes the fallback is resolved.
- Language-pack follow-ups: ut-plugin-language-de / -es / -pt.

## TDD

The new test case failed first (`... = "single-purpose voucher on top-up
replay" has no entry in quarantineReasonKeys`). It passes with the map entry.
The reviewer independently deleted the map line and saw the test fail again.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | nit | ar/fa/tr used literal calques for "single-purpose" while the rest of the UI says "voucher for a specific item". | Fixed: all 4 core locales and the 3 packs use the existing term. |
| 2 | nit | The #5134 sentence sat in the comment above the previous map entry. | Fixed: own comment directly above its entry. |
| 3 | nit | The #4416 review record still said "raw-English fallback". | Fixed: notes it was resolved by #5134. |

The reviewer verified the following and found no blockers:
- The switch order in `sync_sales.go` picks the single-purpose branch for the production error shape (`voucher_repo.go` `TopUpVoucher`).
- The comment's claims (the ≥1.13.0 replica refusal, and that purpose is immutable) are accurate.
- The meta entry is valid.

## Verification

- `go test ./internal/pages -run 'TestPermanentJournalFailureReason|TestSyncQuarantine'`: ok.
- `go test ./...`: see PR.
- `guard-i18n.sh`, `guard-i18n-meta.sh`, and every guard in the ci.yml `build` job: pass. The exception is `guard-shellcheck-version.sh`, because this container has no shellcheck binary and no shell files changed.
- `gofmt -l`: clean. `go build ./...`: ok.
- Packs: `validate.sh` and `check-key-drift.sh`, run against this branch's en.json, show 0 drift.
- No UI layout change: one translated string in an existing table cell, rendered by the existing `TestSyncQuarantinePage` path.
