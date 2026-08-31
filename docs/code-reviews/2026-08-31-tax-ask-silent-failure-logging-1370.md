# Code review — log tax.rate.ask failures instead of failing silently (ut-docs#1370)

- **Date:** 2026-08-31
- **Branch:** `fix/1370-tax-ask-silent-failure-logging`
- **Reviewer:** self-reviewed inline (change is a 2-line, purely-additive
  diagnostic addition with no behavior change; scope did not warrant a full
  independent-model review pass — see rationale below)
- **Verdict:** Safe to merge.

## Context

ut-docs#1370: the German café pilot's live tablet still charges the dine-in
(19%) rate on takeaway sales despite the catalog tax-code assignment and the
plugin's takeaway_rate_overrides setting both being verified correct on that
exact device. A live investigation this session proved, end-to-end, that:

- `universal-till`'s host chain (settings editor, wasm dispatch, caching) is
  correct — a real-chain test using the ACTUAL `ut-plugin-tax-de` manifest
  and a freshly-built `plugin.wasm` from its repo HEAD passes cleanly.
- The bytes the production marketplace currently serves for
  `com.universaltill.tax-de` `0.5.1`/`stable` are correct too — pulled live
  from the marketplace's own blob store and replayed through the same real
  chain, still passes cleanly.
- wazero's Compiler and Interpreter engines agree on the served wasm's
  answer — ruling out an engine-selection difference (relevant because
  Android's release-build hardening can force the Interpreter path).
- The Android release pipeline correctly stamps `buildinfo.Version=0.8.2`
  into `libgojni.so` for the `v0.8.2` tag, and the installed APK pulled
  directly off the tablet does contain that stamp **and** the fix-era
  string literals (`takeaway_rate_overrides`, the ut-docs#368 fail-closed
  log message) — ruling out a stale/wrong Android build.

Every layer testable off the physical device is correct. The remaining,
unconfirmed hypothesis is that `bus.Ask` (or the JSON it returns) is
actually failing on the real Android runtime specifically — and there was
**no way to tell**, because both failure branches in `AskTaxRateBP`
returned "no opinion" with zero log output, identical to the ordinary "no
plugin has an opinion" case.

## What this change does

Adds one `logging.L().Errorf(...)` call to each of the two silent-decline
branches (the `bus.Ask` error path, and the JSON-unmarshal error path),
matching the exact convention `taxAuthorityBroken`'s DB-read failure
already uses a few lines below. No return value, cache behavior, or control
flow changed.

## Review notes

- **No behavior change** — confirmed by diff: only new `logging.L().Errorf`
  statements were added; every `return` is unchanged.
- **No secrets/PII logged** — `l.ItemID`/`l.TaxCodeID` are internal UUIDs,
  `orderType` is a small fixed enum (`""`/`"takeaway"`), and the logged raw
  response is the plugin's own JSON output, not user-entered text.
- **Log volume**: the `bus.Ask` error branch is intentionally NOT cached
  ("transient failure... retry next recompute" — pre-existing comment), so
  a *permanent* failure would log once per uncached ask per recompute
  (i.e., per basket edit needing a line's tax re-resolved), not in a tight
  loop. Accepted as the right trade-off — a bit of log volume during an
  actual outage beats the current total invisibility that let this ship
  undetected.
- **Test coverage**: no new test added. This is a pure logging addition
  with no new branch or return path to cover; the existing
  `internal/pages`, `internal/plugins` and `internal/pos` suites (which do
  exercise both the success and the JSON-unparseable-response paths) all
  still pass unchanged. A dedicated log-assertion test was judged not worth
  the harness complexity (capturing `logging.L()`'s output) for two log
  lines with no behavioral surface.
- **Why self-reviewed rather than a full independent pass**: the standing
  reviewer process exists to catch a *different model's* implementation
  mistakes on real logic changes; this diff has no logic to get wrong. The
  reasoning above is the substance a reviewer would otherwise re-derive.

## Before committing checklist

- `gofmt -l internal/pages/tax_hook.go` — clean.
- `go build ./...` — clean.
- `go test ./internal/pages/... ./internal/plugins/... ./internal/pos/...`
  — all pass.

## What this does NOT do

This does not fix the takeaway-VAT bug itself — the actual root cause is
still unconfirmed. It makes the next occurrence (on Farshid's tablet, or
any other Android till) leave a log line saying exactly which of the two
branches fired and why, instead of nothing at all. ut-docs#1370 stays open
for that.
