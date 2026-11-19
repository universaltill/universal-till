# Review: marketplace Report this listing link (#6400)

Date: 2026-11-18. Lane: sdk. Reviewer: Fable subagent (independent), orchestrator re-verified fixes.

## What shipped (batch: Microsoft Store readiness, epic ut-docs#6380)

| Repo / branch | Content | Cards |
|---|---|---|
| universal-till `feat/6405-6407-msstore-ci` | store guard covers the Microsoft Store EXE API (host, `submission/v1/product`, store-submission action), published-asset overwrite guard (`release-upload.sh`, clobber/delete-asset rules), per-artifact install channel, `msstore-submit.yml` + verify/soak/submit scripts | #6405, #6407 |
| universal-till `feat/6400-marketplace-report-link` | plugin store "Report this listing" + marketplace terms links, English keys only | #6400 |
| ut-cloud `feat/6400-cloud-report-listing` | public report form on the plugin page, `listingreports` | #6400 |
| ut-website `feat/6400-marketplace-terms` | publisher terms + content guidelines DRAFTS (owner approval; do not merge before) | #6400 |
| ut-infra `feat/6407-msstore-entra-oidc` | keyless Entra app + federated credential + gated GitHub environment (main only), plan only | #6407 |
| ut-docs `docs/6400-6402-ms-runbook-assets` | Microsoft Partner Center runbook (owner/Claude steps) | #2480, #6402, #6408 |
| ut-docs `docs/6403-ms-store-assets` | demo-data screenshots, icons, manifest, asset guard, dry-run record | #6403, #6407 |

## Review

Independent Fable review (report kept at /tmp/tabletfix/ms/review-fable.md) plus the Fable early review's lessons for the Play PRs (tag-only environment gate, tests green with protections removed, guards matching names only), applied here.

Findings and outcome:
- H1 submit targeted the MSIX API and a dry run could not test it: **fixed**. Now the EXE/MSI API (api.store.microsoft.com/submission/v1/product/{id}/packages, /packages/commit, /status, /submit, X-Seller-Account-Id, scope .default) from Microsoft Learn; `dry_run=true` is a read-only GET probe; the live path re-checks the verified digest, sets the package, commits, polls readiness, submits. Whether Partner Center accepts a federated (secretless) credential for this API is unconfirmed: the read-only probe is the proof (runbook 12.4).
- H2 nobody can read a submitted report: **accepted + card**. Copy no longer promises review ("We review every report" removed); a staff reader/notification is a follow-up that must land before the listing goes public (11.13).
- H3 apply-order trap: **fixed** (credential `depends_on` the environment and policy; README + runbook say apply first, verify protection, then grant the Partner Center role).
- M1 workflow test green with protections removed: **fixed**, 5 mutants re-run killed (needs, defaults, runs-on, extra trigger, interpolation).
- M2 guard: **fixed** (EXE API path, action names, delete-asset / API DELETE, comment-proof switch rule); nightly prune carries a reviewed marker.
- M3 verified bytes not bound to submission: **fixed** (EXPECT_SHA256 re-checked in submit). Replacing `checksums.txt` extend-only: **deferred** (card).
- M4 infra guard/test: **fixed** (resource allowlist, tf.json scan, reviewers, tag policy, flexible credential, API permissions; tftest asserts).
- M5 reporter email in the global audit plane: **deferred** (card: own table + privacy link). M6 en-US links: **fixed**. M7 survivors: **fixed** (body-cap, non-public listing, no-store tests; body-cap mutant killed).
- M8 website drafts: commitments bracketed for the owner; PR is DO NOT MERGE until the owner approves.
- L-items: L1, L3 fixed; L2, L4-L15 deferred or owner decisions, listed in the cards.

Extra verification: shell tests per script, `terraform test` (3 pass), `go test ./internal/api/...` (ut-cloud), full `internal/pages` (till2) ok, asset guard ok. Pre-flight: en.json -> en.meta.json updated (English only); till help topic plugins.md updated, screenshots not regenerated (deferred, card); Partner Center runbook updated; plan prices n/a; ADR n/a (ADR-0194 binding, unchanged).

Verdict: approve for merge after CI, with the deferred items filed as Backlog cards. Nothing was submitted, no Partner Center call, no secret read.
