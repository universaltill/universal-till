# Review — AI hosted-provider notice + help copy (ut-docs#2835)

**Date:** 2026-10-11 · **Author:** Opus 5.5 (lane:sdk) · **Reviewer:** Fable (independent subagent)

Part of the ut-docs#2835 change set. ut-plugin-integration-ai 2.3.0 adds the
Claude Ask tool loop and `provider = openai_compatible`
(ut-plugin-integration-ai#17; full record:
`ut-plugin-integration-ai/docs/code-reviews/2026-10-11-claude-ask-openai-compatible-2835.md`).

## What changed here

- `plugins.settings.ai.hosted_provider_notice` (en/ar/fa/tr): Ask your till is now sent to the hosted provider with claude too. It adds the openai_compatible sentence: the same data, without reference photos, goes to whatever server `endpoint` names.
- `web/help/{en,ar,fa,tr,de}/plugins.md`: one added sentence on openai_compatible in the AI Assistant paragraph.
- A value change only: no new key, so no lang-pack-drift. Packs de/es/pt are updated in their own PRs.

## Review

The Fable reviewer read this copy against the plugin code. It found:
meaning parity with the English (hosted = claude or openai, Ask now covered
for both, the openai_compatible sentence present), no placeholders, product
names untouched, and none of the old "with claude nothing is sent for Ask"
claim left. No findings on this repo's diff.

## Verified

`go test ./internal/pages -run 'PluginSettings|Notice|Help'` (the notice renders above api_key); guard-i18n, guard-help-drift, guard-help-topics, guard-compliance-claims, guard-competitor-naming all ok. Visual: none (copy-only change to an existing notice block).

**Verdict:** safe to merge.

## Follow-up: docs-shots

CI's docs-shots guard flagged the changed help topics (en/fa/ar/tr `plugins`). Ran `make docs-shots` on the branch (120 passed). The guard and its self-test are green locally. The run also re-took catalog/categories images that main's surface had moved; the regenerated manifest covers the whole tree.
