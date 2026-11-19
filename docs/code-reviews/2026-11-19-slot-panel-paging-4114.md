# Review: slot panel in-place paging (addendum to 2026-11-11-slot-panel-paging-4114.md) (ut-docs#4114), combined Tester+Reviewer pass 2026-11-19

Batch: core-b (5909, 4391, 4114, 5660). Reviewer: Fable (independent of the Sonnet-built code), plus the Tester's targeted runs (no full suite: the orchestrator runs the batch gate).

## Findings
No findings. servePluginEntry runs the host gate before any ask on every method; panel GET asks under ui:slot:<slot>; HX-Target must match this entry's slot prefix (else 400, no ask); mismatched slot query refused; admin.pages excluded; no-store; link values escaped by html/template. Tests assert status and that the plugin was not asked (lastEv.Type) for cashier, missing grant and refusal.

Verdict: PASS, no fixes. Open blockers: none.


Pre-flight: no new user-visible strings or pages in this pass; en.json/en.meta.json only (ADR-0160); no file writes or cwd paths added.
