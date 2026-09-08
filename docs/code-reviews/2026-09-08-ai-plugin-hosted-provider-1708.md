# Code review: AI Assistant plugin opt-in hosted-provider key (ut-docs#1708 / ADR-0085)

- **Date**: 2026-09-08
- **Card**: ut-docs#1708 (product owner request, 2026-09-07)
- **Design**: `ut-docs/adr/0085-ai-plugin-opt-in-hosted-provider.md`
- **Complexity**: hard — built by a Fable subagent, reviewed independently by Opus
- **Repos**: `universal-till` (branch `feat/1708-ai-plugin-hosted-provider-key`),
  `ut-plugin-integration-ai` (branch `feat/1708-hosted-provider-settings`),
  `ut-docs` (branch `docs/1708-adr-0085-ai-hosted-provider`, ADR + `architecture/ai-plugin.md` amendment)

## What shipped

A shop can set the AI Assistant plugin's `provider` setting to `claude` and
enter its own Anthropic API key (`api_key`, secret-typed per ADR-0082) to use
a hosted backend for camera item identification instead of self-hosted
Ollama. Self-hosted stays the default and unaffected for every existing
installation. Only the exact string `"claude"` selects the hosted vendor —
any other value (unset, `self_hosted`, a typo, a different case, or a
provider this build doesn't implement) falls back to the self-hosted-or-
disabled path, so a misconfiguration can never forward a stored key to a
paid API by accident. A data-protection disclosure renders above the
`api_key` field, stating what leaves the shop, before a key can be saved.

No new crypto/masking code — the diff declares `api_key` as `type: "secret"`
in the plugin manifest and reuses ADR-0082's existing seal/open and display-
masking infrastructure unchanged.

## Independent review (Opus, different model from the Fable implementer)

**Verdict: SAFE TO MERGE.** Full findings list, triaged:

| # | Finding | Severity | Outcome |
|---|---|---|---|
| 1 | `lang-pack-drift` needs `ut-plugin-language-{de,es}` updated or `main` goes red on push | should-fix | **Fixed** — both packs updated with real translations (see below), staged to merge immediately after this PR |
| 2 | Disclosure omitted that SKUs (not just item names) leave the shop | nit | **Fixed** — `en/ar/fa/tr` locale strings and the plugin README updated |
| 3 | `" claude "` (whitespace-padded) resolves to hosted — correct behavior (shared `TrimSpace`), but untested | nit | **Fixed** — `TestAIResolve_WhitespacePaddedClaudeStillSelectsHosted` added, pins it deliberately |
| 4 | The new masking test seeded an empty `api_key`, so it couldn't distinguish "masked" from "nothing to leak" | nit | **Fixed** — seeded a real value (`sk-ant-should-never-render`) and added an explicit absence assertion |
| 5 | `settingNoticeKey`'s doc comment didn't flag the coupling to the `.Secret` template branch | nit | **Fixed** — comment updated |
| 6 | Two unrelated screenshots churned (`make docs-shots` nondeterminism) | informational | Accepted as-is — reviewer opened and visually confirmed both render correctly, no real data |
| 7 | ADR-0085 incorrectly claimed `claudeProvider` already has a tool-calling `ask` loop | should-fix | **Fixed** — ADR corrected; `claudeProvider` has vision `identify` only, `CanAsk()` is false for Claude (already correctly reflected in the implementation and its tests). Follow-up filed as ut-docs#1792 |
| 8 | Field render order puts `api_key` (with its notice) before `provider` (alphabetical `ORDER BY key`) | informational | No action — notice text is self-describing regardless of position |

Commands the reviewer actually ran (not just read): `gofmt -l .`, `go build
./...`, `go vet ./...`, `go test ./...` (all packages green), `golangci-lint
run ./internal/pages/... ./internal/ai/...` (0 issues), the full
`TestAIResolve_*`/`TestPluginSettingsPage_GET_AIPlugin*` suite individually,
`guard-i18n.sh`, `guard-compliance-claims.sh`, `guard-help-topics.sh`,
`guard-docs-shots.sh`, `guard-data-access.sh`, `guard-page-http-error.sh`,
and `scripts/validate.sh` in the plugin repo.

## Verified beyond automated tests

- **TDD claim re-verified twice, independently**: the orchestrator (Sonnet)
  personally reverted `aiPluginConfig`'s exact-match check to a fuzzy
  substring match and confirmed `TestAIResolve_UnrecognizedProviderNeverSelectsHosted`
  genuinely fails (7/7 subtests) before restoring the real fix and confirming
  it passes again. The independent Opus reviewer separately reasoned through
  which mutants the test table kills (`Contains`, `HasPrefix`, `EqualFold`,
  `!= "self_hosted"`, fuzzy edit-distance) and confirmed every plausible
  wrong implementation is caught by at least one case, each asserted twice
  (with and without a sibling Ollama endpoint).
- **Security**: grepped for `apiKey|APIKey` across the changed Go files —
  confirmed it never reaches a log statement, an error message, or an HTTP
  response. The settings page still blanks the value before render
  (`sv.Value = ""`) and the input is hardcoded `value=""`.
- **Manifest upgrade path**: confirmed `ReconcilePluginSettings`
  (`internal/data/plugin_repo.go`) inserts newly-declared settings on
  existing installs while preserving already-configured values — without
  this, an existing 1.0.2 install would never get the new `provider`/
  `api_key` rows and the feature would be unreachable.
- **No real shop/client name, no literal credential**: grepped for
  `sk-ant-`/`sk-[A-Za-z0-9]{16,}` — all hits are self-evidently fake test
  fixtures (`sk-ant-shop`, `sk-ant-shop-own-key`, `sk-ant-should-never-*`).
- **Translations**: real German and Spanish translations were written for
  the language packs (matching this repo's existing feature terminology —
  "Per Kamera erkennen"/"Fragen Sie Ihre Kasse" in German,
  "Identificar con la cámara"/"Pregunte a su caja" in Spanish) and validated
  against `check-key-drift.sh` run locally against this branch's own
  `en.json` (no orphan/empty/identical-to-English flags on the new key —
  the drift check's only findings were 3 pre-existing, unrelated orphan keys
  from other in-flight work, `tables.claim.*`, not introduced by this
  change). **Neither the Arabic/Farsi/Turkish nor the German/Spanish
  translations have been confirmed by a native speaker** — flagged
  explicitly by both the Dev implementer and the independent reviewer;
  recommended before this reaches the DE/EU pilot for real.

## Deferred / explicitly out of scope

- **ut-docs#1791**: new OpenAI provider (net-new `internal/ai` backend) —
  `provider` accepts `openai` as a forward-compatible but unimplemented
  value today; falls back to self-hosted.
- **ut-docs#1792**: Claude tool-calling `ask` loop — "Ask your till" stays
  self-hosted-only until built; correctly degrades (`CanAsk()` false) today.
- Pre-existing `tables.claim.*` orphan-key drift in the language packs,
  found while validating this change — unrelated to this card, not fixed
  here.

## Merge plan

Per this repo's own established convention (a new core locale key's
`lang-pack-drift` is advisory on a PR, blocking on push to `main`): merge
this PR first, then `ut-plugin-language-de`/`ut-plugin-language-es`
immediately after, in that order.

## Safe-to-merge verdict

**SAFE TO MERGE.** All reviewer findings addressed (fixed or explicitly
accepted with reasoning above); full gate green.
