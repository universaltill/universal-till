# 2026-09-26 — Allow @mahshid76 in the commit-attribution guard (ut-docs#2953)

## What shipped
`ALLOWED_IDS` gains `111438931 # mahshid76` in `guard-commit-attribution.sh`, plus one `expect_pass` self-test case for `111438931+mahshid76@users.noreply.github.com`. The same change goes to all six repos that carry this guard: ut-docs, universal-till, ut-cloud, ut-my-shop, ut-admin-app and ut-infra.

## Authorization
The product owner approved it in the local session (2026-09-26). The card had been parked in Admin Review because a developer must never add their own ID to this allowlist. Onboarding was approved in #2578. The ID was checked independently: `gh api users/mahshid76 --jq .id` = 111438931.

## Review
Independent reviewer (Sonnet 5; the change was written by Opus 5.5):
- ID correct; array syntax valid; the diffs are identical in every repo.
- Self-tests pass in all repos.
- The new case would fail without the ID: it would fall to the "well-formed noreply ID matching no known contributor" rejection.
- **Finding (fixed):** ut-infra carries the same guard and was missing from the card's five-repo list. It is now included.

## Verified
- `bash scripts[/ci]/guard-commit-attribution_test.sh` is green in each repo.
- The new case was confirmed failing with the ID removed (ut-cloud), then restored.

## Exposure
This only lets commits authored as @mahshid76's own GitHub noreply address pass CI. It grants no repository access; that stays with GitHub team permissions.

## Verdict
Safe to merge.
