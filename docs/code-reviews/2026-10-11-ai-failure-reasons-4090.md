# Review — identify overlay and manual for named AI failures (ut-docs#4090)

- **Date:** 2026-10-11
- **Built by:** Claude Opus 5.5 (inline, lane:cloudsession)
- **Reviewed by:** Claude Fable 5.1 (independent subagent), together with
  the plugin change
- **Card size:** medium (the bulk is in `ut-plugin-integration-ai` 2.3.1,
  universaltill/ut-plugin-integration-ai#18, review record
  `docs/code-reviews/2026-10-11-provider-error-reasons-4090.md` there)

## What shipped

- **The AI plugin now answers identify with an error notice naming the
  reason** (key rejected, no credit, rate limited, unreachable) instead of
  failing the job. Core's `identify_result` used to render that notice and
  then "No match found" under it. `identifyResult.Failed` (an error notice
  with no matches) now suppresses the no-match line.
- **`web/help/en/reports.md`, "Ask your till":** the failure paragraph says
  which reasons the panel names, for which providers, and that identify
  names them too. The section exists only in English; the translated help
  has no Ask section (help-drift baseline, ut-docs#341).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | major | The overlay showed both the error notice and "No match found". | **Fixed.** `TestPluginIdentify_ErrorNoticeIsNotNoMatch_4090` failed first ("an error notice also rendered no match"), then passed. |
| 2 | minor | The help wording didn't match the behaviour (which reasons appear for which providers). | **Fixed.** |
| 3 | nit | A failed identify document still parks the photo in the pending-confirm slot until its TTL. | **Accepted.** No reference photo is stored without a pick. |

## Verified

- `go build`, `go vet` and `go test ./internal/pages` pass, and
  `golangci-lint` reports 0 issues.
- These guards pass: help-drift, help-topics, i18n, no-showmodal,
  no-inline-handlers, compliance-claims, competitor-naming, data-access and
  core-neutral.
- There are no new locale keys, so there are no language-pack follow-ups.
- **Not driven in a browser.** The change is a single template condition,
  and the handler test asserts the rendered HTML.

**Verdict:** safe to merge.
