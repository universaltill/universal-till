# Review: AI data classes, ai.send.* gate, install grants (ut-docs#2837)

- **Date**: 2026-10-12
- **Card**: universaltill/ut-docs#2837 (slice agreed on the card; rest split
  to #4142, #4143, #4144, #4147; security bug #4145)
- **Design**: ADR-0153 Decisions 1 and 5
- **Author**: Opus 5.5 (dev subagent) · **Reviewer**: Fable (independent)

## What shipped

- `data.CoreView.DataClass` on every core read view, with exported class
  constants and `AIDataClasses`. Test: every view has a known class.
  `audit.summary.v1` is `staff` because each row carries `actor_id` (ADR's
  more-sensitive rule). ADR-0153's table lists it as `till_health`; the ADR
  delegated this check to #2837.
- The allowlist recognises `ai.agent`, `ai.send.<six classes>`,
  `ai.tools.write:<catalog|stock|settings|plugins>`, `ai.tool.provide` and
  `ai.tool.provide.write`, checked against closed sets.
- `view_query`: a plugin that **declares** `ai.agent` also needs
  `ai.send.<view class>` on every call. A view with no class is refused and
  audited; `shop` needs no grant.
- `item_image_open` gets the same gate under class `catalog` (review M1).
- Marketplace install (ADR-0006 as amended by Decision 5):
  `ai.tools.write:*`, `ai.tool.provide(.write)`, and `ai.send.*` on a
  non-`ai.agent` plugin are never granted. On an `ai.agent` plugin,
  `ai.send.*` is granted only on its first declaration, so a revoke survives
  updates. An unreadable permission list fails closed.
- Consent descriptions for the new permissions in en/tr/ar/fa, plus the
  de/es/pt packs.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| M1 | major | `item_image_open` returned catalog photos to an ai.agent plugin without `ai.send.catalog` | **Fixed**: same gate, fails closed on an unreadable manifest; TDD test `TestItemImage_AIAgentNeedsAISendCatalog` failed first (`revoked = 0, want -2`) |
| M2 | major | `sale.completed` and other event payloads reach an ai.agent plugin with no `ai.send.*` check (an ADR gap: D1 binds only reads) | **Deferred** to #4147 (needs an ADR amendment). #4142 (AI plugin declares ai.agent) is `blocked:dep` on it. No exposure today: no plugin declares ai.agent yet |
| m1 | minor | Drop-then-re-add across two releases re-grants a revoked `ai.send.*` | **Accepted**, documented in `installGrantsPermission` (the re-declaring manifest passed review again) |
| m2 | minor | ut-cloud `permlint.go` doesn't know the new names and has no reviewer flags for them | **Deferred** to #4142 (comment) |
| m3 | minor | Unticking the `ai.agent` grant has no effect, because the gate keys off declaration | **Deferred** to #4144 (UI shows the six data boxes as the control) |
| n1 | nit | en `ai_send_staff` said "activity log"; the UI term is "Audit trail" | **Fixed** |
| n2 | nit | "roles" in the staff description, though no view returns roles yet | Accepted (follows ADR table) |
| n3 | nit | `aiAgent` cached per event | Accepted, documented on `loadManifestFacts` |

## Verified beyond the automated tests

- I re-checked the TDD claim myself: with the gate call in `hostViewQuery`
  disabled, `TestViewQuery_AIAgentNeedsAISendGrant` fails
  (`ai.send.sales revoked = 2, want -2`). Restored, it passes.
- `LookupCoreView`/`RunCoreView` have no other callers. `item_image_open` was
  the only other core-data host function (review).
- No exposure change for shipped plugins: the published AI plugin doesn't
  declare `ai.agent`, so its behaviour is unchanged until #4142.
- No file writes and no SQL outside `internal/data` in non-test code.
- Not checked visually: the new consent lines render through the existing
  store-card/permission description path, and no published plugin declares
  these permissions yet. The screenshot belongs to #4144.

## Verdict

Safe to merge. Language-pack PRs (de/es/pt) follow core in the same cycle.
