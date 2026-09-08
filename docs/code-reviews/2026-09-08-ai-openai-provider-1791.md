# Code review: OpenAI hosted AI provider (ut-docs#1791)

- **Date**: 2026-09-08
- **Branch**: `feat/1791-ai-openai-provider`
- **Card**: universaltill/ut-docs#1791, ADR-0085 follow-up
- **Author (Dev)**: Sonnet subagent, orchestrated by the `:54` cloud
  scrum-master lane
- **Reviewer**: Opus subagent, fresh context, `isolation: "worktree"`

## What shipped

A third `internal/ai` backend, `openaiProvider`, implementing both the
`provider` interface (`identify`, camera item recognition) and the `asker`
interface (`ask`, "Ask your till" tool-calling) against OpenAI's real Chat
Completions REST API via plain `net/http`/`encoding/json` — no new SDK
dependency, matching `ollama_ask.go`'s raw-HTTP style rather than
`claude.go`'s SDK style, since no OpenAI Go SDK is currently a project
dependency and adding one was out of scope for this card.

Wired through `internal/pages/ai_resolve.go`'s existing plugin-settings
resolution: `provider = openai` (exact-match, fail-safe — any other value,
including `Openai`/`OPENAI`/a typo, falls back to self-hosted) reuses the
existing `vision_model`/`ask_model`/`api_key` settings, no new manifest
fields. Unlike `claudeProvider` (identify only — its `ask` loop is tracked
separately as ut-docs#1792 and deliberately not built here), `openaiProvider`
implements both from day one, so `Service.CanAsk()` is true for a shop on
`provider = openai`.

Model default: `DefaultOpenAIModel = "gpt-4o-mini"` (real, current,
vision+tool-calling capable), used for both `vision_model` and `ask_model`
when left unset — independently overridable, same as every other provider.

Reworded the existing `plugins.settings.ai.hosted_provider_notice` i18n key
(en/fa/tr/ar — this is a rewording of an existing key, not a new one, so
`lang-pack-drift`/external language packs are unaffected) and the matching
`web/help/*/plugins.md` sentence, since the data-protection story is now
per-vendor (Claude: identify only; OpenAI: identify + ask).

Companion PR in `ut-plugin-integration-ai` (manifest version bump 1.1.0 →
1.2.0, README, CLAUDE.md) documents the second provider value — no manifest
schema change (no new settings, no new field type).

## Independent review (Opus, fresh context, isolated worktree)

Full report kept in the PR/session record; summary here.

**Verified correct, by actually running things and probing for a bypass**
(not just reading the diff):
- Real OpenAI wire format audited both directions: `tool_calls[].function
  .arguments` is a JSON-encoded **string** (not an object, unlike Ollama) —
  confirmed handled correctly parsing the response AND re-echoing the
  assistant message verbatim on the next round, with the matching
  `tool_call_id` on each tool-result message.
- `response_format: json_schema` (strict mode) shape and the `data:` URL
  vision-input construction are correct.
- Fail-safe provider matching: actively tried to find a bypass (case
  variants, a JSON-array settings value, duplicate setting rows) — none
  found; everything but the exact string `"openai"` falls through to
  self-hosted, even with a key present.
- No regression to the existing `ollama`/`claude` paths: full `go test
  ./...` green across 51 packages before AND after the review's own
  sabotage-and-restore pass.
- The two recurring bug classes this pipeline has shipped before (a
  file-write handler missing `os.MkdirAll`; a cwd-relative path where
  `paths.Data(...)` belongs) were checked and confirmed not applicable —
  this diff makes zero filesystem calls.
- i18n: all four locale values for the reworded key read as complete,
  accurate translations of the new meaning, not stale/truncated/wrong-
  language text.
- No secrets, no real client/shop name.

**TDD re-verification, done for real**: the reviewer temporarily broke
`runOpenAIToolCall`'s JSON-string handling (reverted it to treat
`Arguments` as if it were already a map, matching Ollama's shape) and
re-ran `TestOpenAIAskToolLoop` — it failed with the expected decode error,
not a compile error unrelated to the claim. Restored, all 8 `TestOpenAI*`
tests pass again. The test is a real regression guard, not a false pass.

**Findings, triaged:**

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Blocker | `README.md`'s `UT_AI_PROVIDER`/`UT_AI_API_KEY` env-var docs and the feature checklist line still said "claude" only, with "no ask loop yet" — now false for the env var and inaccurate for the feature line, and this repo's own `CLAUDE.md` requires the README kept current in the same session. | **Fixed** — both lines updated. |
| 2 | Real, minor | `openai.go`'s non-2xx handling echoed the raw response body upstream into `log.Printf`; OpenAI's 401/403 body embeds a masked fragment of the caller's own key (`sk-proj-***abcd`), and CLAUDE.md says no secrets in logs. | **Fixed** — 401/403 now return a generic "authentication failed" message instead of the raw body; every other status still includes the body for diagnosis. |
| 3 | Real, minor | `ask()` never checked `msg.Refusal` (identify() does) — a mid-loop refusal fell through to a generic "empty model response" instead of the accurate "model declined the request". | **Fixed** — added the same check `identify()` already has. |
| 4 | Real, minor | `ut-docs/architecture/ai-plugin.md` (cross-repo, doc-of-record for this settings surface per ADR-0085) still described only `self_hosted`/`claude`. | **Fixed** in the same session (ut-docs repo, not this PR — see that repo's own commit). |
| 5-7 | Nitpick | Missing `omitempty` on a `Type` field; a theoretical empty tool-result content edge case; `gpt-4o-mini` will age as a default. | Accepted as-is — none are reachable today given `runAskTool`'s existing behavior, and the model is independently overridable via settings. |

**Verdict**: safe to merge after fixing the blocker (done, this commit).

## Verified beyond automated tests

- Full `go test ./...` (51 packages), `gofmt -l .`, `go vet ./...`,
  `golangci-lint run ./...` (0 issues) all re-run and green after the
  review-fix commit, independently by the orchestrating session (not
  just trusting the Dev/Review subagents' self-reports).
- `guard-i18n.sh`, `guard-compliance-claims.sh`, `guard-help-topics.sh`,
  `guard-data-access.sh` all pass.
- `TestPluginSettingsPage_GET_AIPluginAPIKeyShowsHostedProviderNotice`
  reads the notice text dynamically from `en.json` and renders it through
  the real settings-page HTTP handler — a real request, not a mock,
  confirming the reworded copy actually reaches the page.
- No live call to the real OpenAI API was made or is needed — the offline-
  first/no-live-network-dependency principle for this feature is
  unaffected, and the httptest-server test suite doesn't touch the network
  either.

## Explicitly deferred (not this card)

- A Claude `ask` tool-calling loop — ut-docs#1792, separate card,
  explicitly must not depend on this one.
- Native-speaker review of the fa/tr/ar rewording (flagged by Dev; Opus
  spot-checked and found no obvious problems, but neither pass is a
  native-speaker sign-off).
- A manifest "select" field type for `provider` — two, now three, values
  is still judged low enough cardinality for a documented plain-text
  setting (ADR-0085's own Non-goals, unchanged by this card).
