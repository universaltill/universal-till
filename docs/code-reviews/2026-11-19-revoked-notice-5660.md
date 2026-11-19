# Review: revoked-plugin notice on /plugins (ut-docs#5660), combined Tester+Reviewer pass 2026-11-19

Batch: core-b (5909, 4391, 4114, 5660). Reviewer: Fable (independent of the Sonnet-built code), plus the Tester's targeted runs (no full suite: the orchestrator runs the batch gate).

## Findings
| id | sev | finding | outcome |
|---|---|---|---|
| M1 | medium | install_state revoked is never cleared except by update/reinstall: if the store lifts a revocation the card keeps saying Withdrawn, and a re-enabled plugin is skipped by ListAutoStartPlugins after restart; this card extends the state to plugins the merchant had already turned off | deferred: clearing on lift is a revocation-semantics decision (pre-existing for active plugins; the audit trail is the point of recording); backlog card to be filed by the orchestrator |
| L1 | low | revokedVersion/revokedReason/revokedAt sent to the page, unused by the template | accepted: harmless, reason is capped and control-stripped, rendered with x-text only |
| L2 | low | key plugins.revoked.hint_unknown breaks the plugins.manage.revoked_* naming | accepted: renaming churns Weblate keys |
| T1 | low | body assertions in plugins_page_revoked_5660_test.go are static template strings; only the JSON item checks carry weight; no e2e spec shows the notice | accepted: the item JSON assertions (revoked flag, reason) are the real checks; e2e gap stated |

Verified: page is manager-gated, a failed notice query degrades to no notice, nothing touches the sale path.

Verdict: PASS with one deferred medium (M1). Open blockers: none.


Pre-flight: no new user-visible strings or pages in this pass; en.json/en.meta.json only (ADR-0160); no file writes or cwd paths added.
