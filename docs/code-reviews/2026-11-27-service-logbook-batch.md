# Review: service logbook batch (epic ut-docs#3691)

Scope: universal-till PR #2783 (ut-docs#3697, core plugin-records channel) and
ut-plugin-page-service-logbook PR #1 (ut-docs#3700-#3705, #7108 plugin half).
Author model: Sonnet 5.5. Reviewer: Fable (independent, money/privacy/security).
Date: 2026-11-27. Pre-flight: core n/a for user-visible behaviour (inert without
`records_forward`); en.json + en.meta.json only; ut-cloud mirror, host-function
reference and contract doc still owed (see Deferred); plugin help topic owed.

## Verified clean
Endpoint auth (syncTill bearer, lockout, 403 not_main, sender from bearer only,
exact-path exempt, body cap after auth); plugin id from calling module and
collections checked against its own manifest on both sides; payload, batch,
outbox and snapshot caps; SQL parameterised in internal/data; no new file writes
(MkdirAll/paths.Data n/a); default-off; money int64 minor units per currency;
CSV injection guard; no real shop names/secrets in test data.

## Findings and outcome
| # | Sev | Finding | Outcome |
|---|---|---|---|
| B1 | blocker | Replica could never log an entry (staff validated against empty local table) | Fixed (plugin afb8823): snapshot validation, names from snapshot, tests use snapshot only |
| B2 | blocker | Merging plugin PR auto-tags and publishes | Fixed (c4dd75a): tag/release gated on repo var PUBLISH_ENABLED=='true', default off |
| B3 | blocker | Core PR conflicting, no CI | Fixed (5d0db987c): one merge of main, docs-shots hash regenerated, migration 120 still free |
| B4 | blocker | Merge order: ut-docs contract/host-fn rows/ADR notes and ut-cloud `records_forward` mirror must land first | Deferred, pre-merge dependency (below) |
| H1 | high | Linked-mode ignored on replica | Fixed: single EffectiveMode() |
| H2 | high | Sale linked on replica and main counted twice | Fixed: Apply rejects `already_linked` |
| H3 | high | GB hard delete resurrected another person's superseded entry | Fixed: delete walks supersedes chain |
| H4 | high | GB erasure incomplete (late replica records, no replica trim, archived GB delete) | Fixed: `unknown_staff` terminal reject, replica trim at 35 days, archived GB delete allowed |
| H5 | high | Admin page worked on replica | Fixed: notice + every admin action refused |
| H6 | high | No help topic for three new pages | Deferred: release gate (publish is off), card to file |
| M1 | med | Uninstall left snapshots/outbox/inbox | Fixed (e4b1fa69e) |
| M2 | med | till_promoted rejections never swept or surfaced | Fixed (d60dbde38): hourly housekeeping on any role |
| M3 | med | records_status returned oldest 200 | Fixed (f9209f033): rejected first, then newest |
| M4 | med | Role()/Lines error silently dropped forwarding | Fixed: pending + retry |
| M5 | med | Edit moved entry to today's business date | Fixed: keeps date and currency |
| M6 | med | Linked totals vs sale discount; partial link | Partly fixed: validate-all-then-write, rollback, negative line refused. Sum-vs-total_minor deferred (receipt discount_minor not spread onto lines) |
| M7 | med | Manifest uses permissions/fields core lacks (payout:suggest, tips.suggest.ask, shop_types, shop.context.v2 country/business_date) | Deferred: known release gate (dev notes), publish off |
| L | low | reason length cap (fa65a8cfc); amount upper bound 1e11; edit_start author/manager check | Fixed |
| L | low | Push-tick delay, backoff not reset, dedupe race (idempotent), archived tips under `unmapped`, admin form prefill/40-item cap, Receipts() N+1, partial-vs-whole month compare | Deferred to cards / epic checklist |

## Extra verification
Reviewer reproduced B1, H1, H2, H3 with failing probe tests in a throwaway
worktree. Fixers wrote each test first and saw it fail (23 plugin tests red
before fix; each core fix has a `_3697` test red then green). Gates: core gofmt,
build, vet, golangci-lint (touched pkgs), tests for data/auth/db/server/
housekeeping/plugins, pages filtered run, guard scripts, sdk/plugin incl. wasip1;
plugin gofmt, vet (+wasip1), go test, build.sh, validate.sh, i18n guard, shellcheck,
actionlint, package.sh. Not run: full internal/pages suite (CI will).

## Decisions to confirm
- Main accepts records for archived (non-GB) staff; rejects only unknown/erased.
- GB delete also removes older replaced rows belonging to another member (no longer counted anywhere; extra data loss accepted for erasure correctness).
- M1 records cleanup runs after the uninstall commit (best effort, like plugin_storage); main refuses later pushes as `plugin_inactive`.

## Verdict
Core: approve after CI on the merged branch, **blocked from merging** until ut-docs
D0 (contract plugin-records.md, host-function rows, ADR-0121/0149 notes, new
records_status order) and the ut-cloud `records_forward` mirror
(CanonicalManifest, TestCanonicalManifestMirrorsPOS, collection-name validation)
have landed. Plugin: approve to merge (publish gated off); do not set
PUBLISH_ENABLED until #3157 slice B, core channel, M7 and legal review land.

## Deferred
Cards: help topic (H6); manifest vs core capability gaps (M7); M6 sum check; low items above.
