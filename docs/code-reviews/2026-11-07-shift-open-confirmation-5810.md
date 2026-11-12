# Review — open-shift confirmation without the raw shift UUID (ut-docs#5810)

**Date:** 2026-11-07 · **Branch:** `fix/5810-shift-open-confirmation` · **Lane:** `lane:cloud-54`
**Author:** Sonnet (Dev subagent) · **Reviewer:** Opus 5.5 (independent subagent, fresh context)

## What shipped
- `internal/pages/shifts_api.go` `respondShiftSuccess`: the HTML confirmation on
  `/shifts` is now the plain `shifts.open_confirmed` = "Shift opened." instead of
  "Shift opened: <uuid>". The JSON branch is unchanged (`shift_id` still returned).
- New key `shifts.open_confirmed` (en only, ADR-0160 §2) + `en.meta.json` entry
  (`review: standard`, no placeholders). A placeholder change needs a new key, so
  the old `shifts.open_success` ("…%s") is removed from en/fa/tr/ar and its line
  from `scripts/ci/i18n-baseline/key-meta-baseline.txt` — leaving it would keep a
  `%s` string nothing reads (and fa/tr/ar would have rendered `%!(EXTRA …)` had
  they been kept with the new no-arg call).
- Test `TestOpenShift_HTMLSummaryIsTranslated` asserts "Shift opened." and no
  shift id / `%s` / `%!`; the fa half asserts the English fallback until Weblate
  translates the key. `e2e/tests/login.spec.ts` comment updated (it cited the UUID).

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | low | fa half of the test no longer proves the HTML branch uses the request locale (English fallback) | Accepted per ADR-0160 §2; comment added to restore the fa assertion once Weblate delivers fa |
| 2 | nit | `login.spec.ts:759` comment said the message carries the UUID | Fixed |
| 3 | nit | No CHANGELOG entry | Accepted — small UI fixes don't get entries by repo convention |
| UX | — | `shifts.adjustment_success` still prints the adjustment UUID (same class) | Deferred → ut-docs#5814 |
| UX | — | 360px horizontal overflow / result below the fold at 1024×600 | Pre-existing; in flight in universal-till#2426 (ut-docs#5782) |

No remaining reference to `shifts.open_success` outside the frozen record
`2026-09-03-shift-open-i18n-1406.md`. No file writes or paths in the diff
(`os.MkdirAll` / `paths.Data` checks n/a).

## Verification
- TDD re-verified by Tester and Reviewer independently: with `shifts_api.go` reverted
  the test fails with `shifts.open_success%!(EXTRA string=<uuid>)`; restored, it passes.
- Gate: `gofmt -l .` clean, `go build ./...`, `go vet` (touched pkgs), `golangci-lint
  run ./...` 0 issues, `go test ./...` green except `internal/plugins`
  `TestHTTPSettingBoundGrant` timing out under parallel guard load (no diff there;
  passes alone in 28s); every `ci.yml` build-job guard passed except
  `guard-deadcode-baseline.sh` (local go1.26/1.27 toolchain mismatch) and
  `guard-shellcheck-version.sh` (no shellcheck binary locally) — no shell or Go-API
  surface changed; CI runs both.
- Driven run (Chromium, `e2e/run-till.sh`): opened a shift on `/shifts` at 1024×600 and
  360px — `#shift-result` reads "Shift opened.", one line, no UUID
  (`assets/5810/shift-result-1024.png`, `shift-result-360.png`, looked at). fa (RTL)
  shows the English fallback with no key name or `%!` artefacts. JSON path still
  returns `{"data":{"shift_id":…,"success":true},"error":null}`.
- UX §7 gate: pass. Not checked: real touch hardware, Pi/Wayland kiosk mode, WebKit,
  screen reader, long locales (string is shorter than before).

Pre-flight: help topic n/a (no topic quotes the message; behaviour text unchanged) ·
docs-shots n/a (confirmation is transient, not in any shot) · `reference/` n/a ·
README n/a · `en.meta.json` updated · other locales: old key removed, new key via Weblate.

**Verdict:** approve — merged after fixes 2 and the finding-1 comment.
