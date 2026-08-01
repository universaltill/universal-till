# Review: truncate/redact `.ask` event results before the wasm INFO log (ut-docs#202)

**Date:** 2026-08-01 · **Branch:** `fix/truncate-wasm-ask-result-log-202` · **Card:** universaltill/ut-docs#202 (p2, found during independent review of ut-docs#189's export/report plugin dispatcher, universaltill/universal-till#136)

## What shipped

`internal/plugins/wasm_runtime.go`'s `HandleEvent` used to log every plugin
handler's full stdout response at INFO level unconditionally
(`logging.L().Infof("[wasm:%s] result: %s", pluginID, out)`). Harmless when
the only `.ask`-style hook was `tax.rate.ask` (a tiny `{"rate_bp":N}`
answer); no longer harmless once `export.requested.ask` could answer with a
full exported sales dataset as base64 in `content_b64` — that line was
writing an entire sales export into the till's log file, against
`CLAUDE.md`'s "no secrets in logs" rule and GDPR-adjacent given the
customer-erasure endpoint sits right next to the export one in
`internal/pages/data_api.go`.

1. **`wasmResultLogLine(pluginID, eventType, out string) string`** — the
   single formatted line `HandleEvent` logs. Non-`.ask` events (e.g.
   `sale.completed`) keep the exact original, unredacted format —
   explicitly out of scope. `.ask`-suffixed events (`strings.HasSuffix`)
   route through `safeAskResultForLog`.
2. **`safeAskResultForLog(out string) string`**:
   - Parses `out` as a JSON object; any field whose raw value exceeds
     `maxAskFieldBytes` (200), **or whose key looks like a base64 blob**
     (`looksLikeBlobFieldName` — contains `"b64"`, case-insensitive), is
     replaced with an `"<omitted: N bytes>"` placeholder.
   - Re-marshals and passes the result through `truncateForLog`, which
     hard-caps the final line at `maxAskLogBytes` (500), cutting at a valid
     UTF-8 rune boundary.
   - Non-object output (a malformed/buggy plugin answer) falls straight to
     `truncateForLog`.
3. Doc: `ut-docs/architecture/wasm-runtime.md`'s stdout-logging line updated
   to note the redaction and the `.authorize` gap (below).
4. Follow-up card filed: universaltill/ut-docs#245 (`.authorize`-suffixed
   events — also blocking, value-returning hooks, e.g. payment
   authorization — still log verbatim; same fix class, different event
   suffix, correctly out of scope for this card).

## Independent review

First-pass verdict was **NOT SAFE TO MERGE**. An independent (different-model,
`opus`) review, briefed to actually run things and try to break the fix, found:

- **Blocking — the regression tests didn't cover the call site.** The first
  draft unit-tested `safeAskResultForLog` directly; the reviewer reverted
  *only* `HandleEvent`'s call site (keeping the helper function intact) and
  the entire test suite stayed green — i.e. the actual ut-docs#202 defect
  could have been silently reintroduced with zero test failures. Fixed two
  ways: (a) collapsed `HandleEvent`'s two log branches into the single
  `wasmResultLogLine` call the reviewer suggested as the "cheapest fix", and
  tested that directly (`TestWasmResultLogLine_GatesOnAskSuffix`); (b) went
  further than the reviewer's suggestion and added a **true end-to-end
  regression test**
  (`TestHandleEvent_AskResultRedactedInRealLog`,
  `wasm_runtime_ask_log_integration_test.go`) that runs a real compiled WASM
  module (new `testdata/bigask_guest` fixture) through the real wazero
  runtime and the real `logging` package — actual process stdout captured
  via `syscall.Dup2` fd redirection (reassigning the Go `os.Stdout`
  variable doesn't work here: `logging.go` binds its `*log.Logger` to
  `os.Stdout` once inside a `sync.Once`, well before this test runs, so it
  holds the original `*os.File`; redirecting the underlying fd 1 affects it
  because writes go through the fd number). Re-ran the reviewer's exact
  call-site-revert mutation against this test: it now **fails loudly**,
  showing the full base64 payload in the "captured" log — confirmed the gap
  is closed, then restored the fix (verified byte-identical via `md5sum`)
  and confirmed the test passes again.
- **Non-blocking, same bug class — no overall size cap.** 400 small fields
  (each under the per-field threshold) summed to 65KB passing through
  unredacted. Fixed: `truncateForLog`'s final hard cap now applies whether
  or not any individual field was redacted.
- **Non-blocking, the actual GDPR concern the ticket named — size-only
  redaction missed small blobs.** A *small* `content_b64` (e.g. a two-line
  export with a real customer's name/email) stayed under the 200-byte
  per-field threshold and logged verbatim — byte size alone doesn't capture
  the risk. Fixed: `looksLikeBlobFieldName` now redacts by field-name
  pattern regardless of size.
- **Nitpick — truncation could split a UTF-8 rune** (e.g. an RTL export
  filename), producing invalid UTF-8 in the log. Fixed: `truncateForLog`
  walks back to the last valid rune boundary (`utf8.RuneStart`).
- **Nitpick — the two size constants were mildly incoherent** (per-field
  cap smaller than the non-object fallback cap). Fixed: unified into one
  `maxAskLogBytes` used consistently as the final-line cap.
- **Correctly verified, no fix needed:** the returned `json.RawMessage` (what
  `data_api.go`'s export handler actually acts on) is never touched by the
  redaction — confirmed by code inspection and by
  `TestExportDispatch_RealWasmModule` (existing, unchanged, still asserts
  the exact returned `content_b64` decodes correctly) plus the new
  integration test's own explicit assertion that the returned value
  contains the full, un-redacted payload. No file writes in this diff (the
  `os.MkdirAll` bug class is N/A). No cwd-relative paths (the
  `paths.Data(...)` bug class is N/A). No real shop/client names or literal
  credentials in the diff.
- **Correctly left out of scope:** `.authorize` events (also blocking,
  value-returning, e.g. payment authorization) still log verbatim — same
  fix class, filed as ut-docs#245 rather than silently expanding this
  card's scope.

Full independent-review process note: the reviewer was initially briefed to
restore its probe mutations with `git checkout -- wasm_runtime.go`, which
would have destroyed the (uncommitted) fix, since HEAD was the pre-fix
version — the reviewer caught this itself and backed up to scratchpad
instead. Worth fixing in the `reviewer` skill's own instructions before the
next review that mutates an uncommitted file.

## Test plan

- TDD throughout: every test written and confirmed failing against the
  pre-fix/pre-refactor code before implementing, per this file's own
  practice.
- Mutation-tested twice: (1) this session's own mutation of the redaction
  condition (`if len(v) > maxAskFieldBytes` → `if false && ...`) confirmed
  `TestSafeAskResultForLog_RedactsOversizedField` fails with the exact
  expected message, then restored; (2) the independent reviewer's call-site
  mutation, re-run against the final fix and confirmed caught by
  `TestHandleEvent_AskResultRedactedInRealLog` (see above).
- `go build ./...`, `go vet ./...`, `gofmt -l` (clean for all changed
  files), `guard-data-access.sh`, `guard-i18n.sh` — all clean.
- `go test ./internal/plugins/...` and full `go test ./...` green except the
  one **pre-existing, unrelated** failure
  (`TestSaveCleansUpDirectoryOnWriteFailure`, `internal/issuereport`) —
  already documented in the immediately-preceding PR
  (universaltill/universal-till#147) as a root-user sandbox artifact, not
  caused by this change; confirmed still the only failure in the full suite
  after this change too.
- 8 tests total in `internal/plugins`: 7 unit tests
  (`wasm_runtime_ask_log_test.go`) covering the gate/format,
  oversized-field redaction, name-based redaction of a small PII-bearing
  blob, the overall-size cap under many small fields, the small-response
  fast path, non-object fallback truncation, and UTF-8-safe truncation; 1
  real end-to-end test (`wasm_runtime_ask_log_integration_test.go`) through
  a real compiled WASM module with real process-stdout capture.

## Verdict

Safe to merge. Blocking finding fixed and re-verified with the reviewer's
own mutation; both non-blocking findings in the same risk class as the
ticket fixed rather than deferred (both were ~3 lines each); nitpicks
fixed; doc updated same-session; one genuinely out-of-scope item (`.authorize`)
filed as a follow-up card rather than silently expanded into this one.
