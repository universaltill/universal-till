# Review: AI assistant settings tools, core half (ut-docs#4129)

- **Branch:** `feat/4129-ai-settings-tools` (universal-till)
- **Author:** Sonnet 5.5 (lane:local, pipeline owner's session)
- **Reviewer:** Opus 5.5, an independent subagent (read-only, ran build/vet/tests and a probe)
- Binding design: ADR-0153 Decision 3 (`settings.set.v1`, allow-list, confirm gate).

## Scope (one vertical slice)

- `internal/data/agent_settings.go`: the settings catalogue (one entry per shop-wide Settings
  region, the screen's own label/help keys, the deep link `/settings#<card id>`, readable or
  not, writable or not) and the `settings.describe.v1` read view (class `shop`, `view:shop`).
- `internal/agentaction/settings_set.go`: `settings.set.v1`, area `settings`, role permission
  `settings`. Writable: `pos.allow_negative_inventory`,
  `catalog_import_barcode_from_sku_default`, `catalog_pre_pack_unit_price_enabled` (bool,
  on/off). Same settings row and the same audit event/payload as `settingsAudit`, in one
  transaction; the value is re-read in the transaction (stale guard); a no-change proposal is
  refused.
- `internal/pages/agent_confirm.go`: `settings` role arm, satellite refusal, live-state
  refresh after commit, card rows with translated item/value (`ItemKey`, value kind `key`).
- `internal/plugins/event_dataclass.go`: the Ask job of an `ai.agent` plugin is delivered.
- Guard tests: every Settings region has a catalogue entry; its words exist in every locale;
  its link target exists in `settings.html`; the writable set is exactly the reviewed list;
  nothing forbidden is writable or readable.
- Locale keys (en, tr, ar, fa) + `i18n-meta`; de/es/pt in the language packs.

## Findings and triage

1. **Blocker, fixed.** Declaring `ai.agent` made core withhold the plugin's own Ask job event
   (`<id>.ask`, hop 0, raised by the page layer): "Ask your till" would have stopped working.
   Reproduced first (`TestEventDataClass_AIAgentPageLayerJobDelivered_4129` failed with "has no
   AI data class"), then fixed with `isAskJob`: only a call carrying a `JobCall` and the ask
   operator (core's job path) under the plugin's prefix. The same name without a job, or a job
   without an operator, stays refused (asserted).
2. **High, fixed in the plugin.** `min_pos_version` raised to `0.35.4` (the SDK bump imports
   `ut.action_propose`; the view and action exist only in the core release carrying this PR).
   **The plugin PR must not merge before that core release exists.**
3. **Medium, fixed.** The refresh after commit used `LoadState`, which swallows read errors and
   resets unrelated fields. Now `UpdateState` on the one field that lives in live state.
4. **Medium, fixed.** `update.auto_enabled` described the wrong value when unset; it now has no
   value (the screen's own reader decides, not a generic parse).
5. **Medium, accepted, follow-up.** "Explain all settings" covers the shop-wide regions only;
   tax, fiscal, users, printer, display, theme, backup, tills, plugins and AI screens need
   explain-only entries. The assistant prompt now forbids inventing a link for a setting it
   cannot see.
6. **Medium, fixed.** de/es/pt translations of the four new core keys: language-pack branches
   prepared (new keys can only pass the pack guard after core merges).
7. **Low, fixed.** Uses `SettingsRepo.SetTx` (bearer-file branch included) instead of a copy.
8. **Low, accepted.** "Already in that state" and "not allow-listed" share `ErrInvalid`
   (the plugin words both as "explain the steps"). 9. **Low, accepted.** Audit `item` is empty
   for settings rows (`item_key` carries the label); the setting key is in the manual audit row.

## Verified beyond automated tests

- Mutation: removing the in-transaction stale check fails `TestSettingsSet_StaleRefused_4129`.
- Reviewer probe confirmed the blocker; the fix test reproduces it through a real `Sync`.
- Checked by the reviewer, nothing found: no path to a non-allow-listed key, no secret or PII in
  the describe view, cashier cannot propose or confirm, single-use proposals bound to
  user+session, audit identical to the manual screen, satellite refusal at propose and confirm.
- `view:shop` lets any signed-in operator (cashier included) read the explanations through the
  assistant. Accepted: label/help text and non-secret values only.

## Not verified

No real till device, no browser run of the Ask panel and confirm card (no screenshot taken, so
no UX visual attestation), no run against a real model. Surfaces: the existing core confirm
card (rows only), unchanged layout.

## Verdict

Safe to merge once CI is green. Deferred: explain-only entries for every other screen, link
component for deep links (plugin views have no link component, so the path is plain text),
highlight of the targeted field, effect text on the card, operator-language describe text,
screenshots in chat, satellite messaging in the assistant's words.
