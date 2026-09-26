# Review: cloud_link entitlement tier (ut-docs#2821, ADR-0117 task 1)

- **Date:** 2026-09-26 · **Branch:** `feat/2821-cloud-link-entitlement` (ut-cloud + universal-till)
- **Author:** Sonnet 5 (dev subagent) + orchestrator fix · **Reviewer:** Opus 5.5 (independent, fresh context)

## What shipped
- **ut-cloud:** `subscription.CloudLink` (allowlisted store → realtime unconditionally; else active + plan in `CLOUD_LINK_REALTIME_PLANS` → realtime/always; else periodic). `entitlement` block in `/v1/stores/sync` carries `cloud_link` (+ `cloud_link_mode` when realtime) in every branch incl. no account. Config `CLOUD_LINK_REALTIME_PLANS` / `CLOUD_LINK_REALTIME_STORES` (default empty); `ValidateCloudLink` fails startup on an unknown plan name. Docs: operations.md, env.sample.
- **universal-till:** `entitlement.KeyCloudLinkTier/Mode` (`cloud.link_tier`, `cloud.link_mode`), `Block` parse + normalisers, `Values()` writes them (missing field → periodic; absent block keeps the cache), `entitlement.CloudLink` reader (unconfirmed / stale past Grace / unknown → periodic). `cloud.link_` is a per-till settings prefix (never synced from the main till's admin bundle).

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 0 | medium | (orchestrator, pre-review) allowlist required an active subscription — the test shop has no StoreAccount, so the override was dead; contradicted ADR-0117 §2 amendment. | Fixed; tests flipped (no-account + lapsed allowlisted → realtime). |
| 1 | low | A till demoted to replica keeps `entitlement.last_confirmed_at` fresh via the relay while `cloud.link_*` stay stale → `CloudLink()` can say realtime. No caller yet; ADR §1: replicas never dial. | Carried as an AC on #2824 (dial only as main till). |
| 2 | low | Unknown/capitalised plan in `CLOUD_LINK_REALTIME_PLANS` silently → all periodic. | Fixed: `ValidateCloudLink` at startup + test. |
| 3 | nit | Test header comment contradicted the allowlist rule. | Fixed. |

## Verified beyond the unit tests
- Mutation checks (orchestrator): `CloudLink` forced periodic → subscription + 3 handler tests fail; handler not writing `cloud_link` → 4 handler tests fail; till `LinkTier` accepting anything → fails; staleness check disabled → `TestCloudLinkReader` fails; `Values()` writing periodic → 2 tests fail. Reviewer: removing the per-till prefix → 3 data tests fail.
- ut-cloud `go test ./... -race` green; universal-till `go test ./... -race` green (exit 0); `guard-data-access.sh`, `guard-i18n.sh` green. No UI, no locale keys.

**Verdict:** safe to merge.
