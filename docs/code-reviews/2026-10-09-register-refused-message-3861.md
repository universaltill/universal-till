# Review — Register now: translated message for a refused shop (ut-docs#3861)

## What shipped
- `internal/enroll`: `register()` now returns a typed `*RegisterHTTPError{Status, Code, Body}` for a non-2xx answer from `POST /v1/stores/register`. `Error()` keeps the old `register returned %d: %s` text, so logs don't change. `IsServiceRefused(err)` uses `errors.As` and is true only for 403 + `error.code == "service_unavailable"`. The code is parsed best-effort, so a non-JSON or truncated body leaves it empty.
- `internal/pages/settings_page.go` `POST /api/enrol/now`:
  - The raw error is logged (`logging.L().Warnf`) and is never rendered.
  - A refused shop sees the new key `settings.enrol.service_unavailable`. The message names no country, no reason and no endpoint.
  - Any other failure sees the existing `settings.enrol.failed` plus the HTML-escaped endpoint.
  - Before this change, `err.Error()` was printed unescaped, so the cloud's response body was also an HTML-injection path. It is gone now.
- New key in `web/locales/{en,ar,fa,tr}.json`. Pack PRs for de/es/pt follow, merged after this one.

## Review
- Dev: Sonnet. Independent review: Opus, fresh context.
- Verdict: **safe to merge**, no blocking findings.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Low/Med | The claim-code card (`settings_page.go` ~1096) still shows the escaped raw `claim-code returned 403: {json}`. | Out of scope. Follow-up ut-docs#3990. |
| 2 | Low | A replica's refusal comes back through the main till as a plain error, so it gets the generic message. Nothing leaks. | Follow-up ut-docs#3990. |
| 3 | Low | "Endpoint not configured" and slot-timeout errors now show the generic "check the internet connection" message. | Accepted under the card's acceptance criteria. Follow-up ut-docs#3990. |
| 4 | Nit | The not-registered branch rendered "…try again.: This till is…". | Fixed: the colon is dropped. |

## Verified
- TDD, re-run by the reviewer: with only `settings_page.go` reverted, `TestEnrolNow_ServiceRefusedShowsTranslatedMessageOnly` fails. The rendered body is `register returned 403: {"data":null,"error":{"code":"service_unavailable",…}}`. `TestEnrolNow_OtherFailureShowsGenericMessageNotRawBody` fails too, rendering raw JSON and an unescaped `<b>`. Both pass once the file is restored. The enroll tests don't compile without `RegisterHTTPError`, which is the red step.
- Gates:
  - `go build ./...` and `go vet` on the touched packages.
  - `go test ./internal/enroll/... ./internal/pages/...`.
  - `guard-i18n`, `guard-help-drift`, `guard-help-topics`, `guard-compliance-claims`, `guard-core-neutral`, `guard-data-access`, `guard-page-http-error`, `guard-no-showmodal`, `guard-kiosk-engine`, `guard-competitor-naming`, `guard-card-data-schema`.
- Not run locally: `shellcheck` (not installed, and no shell scripts changed) and `golangci-lint`. CI runs both.
- Translations: the reviewer checked ar/fa/tr for meaning, neutrality and neighbouring terminology.
- Visual: not looked at in a browser. The change only alters the text inside the existing `<span class="error">` fragment, and the handler tests assert that text.
- Help: no topic describes the failure text, so `web/help` is unchanged.

## CI follow-up: seed-22 shuffle failure
- `pages-shuffle (seed 22)` failed on the rebased head: `TestEnrolCheckPlan_UnregisteredOrViaMainSaysNotRegistered` saw "registered through the main till".
- Root cause: `TestSettingsAndStatusBar_ReplicaRegisteredViaMainTill`'s cleanup re-ran `enroll.Init` with its own replica settings, which leaves the package-level enroll state "via main till" for every later test. The two new tests changed seed 22's order, so the leaker started running before the victim. `main` passes only because of its order.
- Proof: with the old cleanup, the pair (`-run` both, `-shuffle=1` and `2`) fails deterministically. With the fix, it passes under seeds 1–4.
- Fix: the cleanup now resets to `emptyKV{}`, as the sibling tests do. The full `internal/pages` package passes with seed 22, with seed 1791386291628608919, and unshuffled.
