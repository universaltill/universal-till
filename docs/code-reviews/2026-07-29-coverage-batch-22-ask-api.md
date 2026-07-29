# Test coverage batch 22: "Ask your till" AI reporting assistant

2026-07-29

`internal/pages/ask_api.go` — manager-gated natural-language reporting:
`POST /api/reports/ask` gates on `CanAsk()` (404 if the AI backend
doesn't support the tool-use loop) then manager then question length,
runs the model against a read-only tool surface (`sales_by_day`,
`top_items`, `payment_breakdown`, `stock_levels`,
`till_activity_summary`), and always writes an `ai_ask` audit row
regardless of outcome. Previously zero coverage.

The tool-use loop mechanics themselves are already thoroughly tested in
`internal/ai/ask_test.go`. This batch deliberately doesn't re-test that
— instead it (a) calls each `askTools()` entry's `Run` function directly
against a real seeded repo to confirm the repo-method/param wiring, and
(b) uses a `fakeAskServer` helper modeled on `internal/ai/ask_test.go`'s
own `askService` pattern — a real `httptest.Server` returning a minimal
Ollama-shaped response, injected via the `Deps.AI` test seam — to
exercise the actual HTTP handler's gating/validation/success/failure
paths end-to-end rather than at the Go-interface-mock level.

## What's covered

- `intArg`: missing key, wrong type, in-range, below/above bounds, both
  boundaries.
- All 5 `askTools()` entries' `Run` functions against real seeded sales
  data, including the out-of-range-days-falls-back-to-default case.
- `CanAsk()` gate returns 404 when AI isn't configured, and — critically
  — 404 even for a non-manager (proving the gate order: `CanAsk` before
  the manager check, not the other way round).
- Manager gating (403) once AI is configured.
- Question validation: empty, whitespace-only, and >500 chars, all 400.
- A real success round-trip through the fake Ollama server: answer
  text rendered, non-error styling, `ai_ask` audit row with `ok:true`
  and the question recorded.
- A real failure round-trip (HTTP 500 from the provider): in-place
  error message (200, not an HTTP error status), error styling, audit
  row with `ok:false` and the question still recorded.
- A distinct failure mode: HTTP 200 but an empty model response — the
  underlying Ollama client treats this as an error too, not a silent
  success; confirmed separately from the 500 case.

## Independent review (opus) — real gap closed, several strengthenings applied

The review verified the fake-server round trip against
`internal/ai/ask.go`'s actual Ollama response parsing (confirmed the
answer genuinely passes through `chat()`'s real decode/error logic, not
just the HTTP transport) and confirmed the audit-payload substring
checks are safe (Go's `encoding/json` sorts map keys alphabetically, so
`"ok":...` appears as a stable token).

The main finding: the original 404 test set `UT_AUTH=off`, so it proved
404 comes uniquely from the `CanAsk` gate, but — with the manager gate
already passing via auth-off — it couldn't actually prove `CanAsk` runs
*before* the manager check, despite its comment claiming that. Added a
second test with auth ON and no manager context: 404 there is only
possible if `CanAsk` really is checked first.

Other fixes applied: an empty-answer-but-200 case (a distinct failure
branch in the underlying Ollama client, previously only the 500-status
case was covered); a whitespace-only question case (exercising the
`TrimSpace` path specifically); the question now asserted as recorded
in the audit payload on the failure path too, not just success; the
manager-gating test now pins `UT_AUTH=on` explicitly rather than
relying on it being unset in the ambient shell; and `context.Background()`
switched to `t.Context()` for consistency with the rest of the harness.

## Verification

`go build ./...`, `go test ./...`, `scripts/ci/guard-data-access.sh`,
`scripts/ci/guard-i18n.sh` — all pass.
