# Review: Android update refusal reaches the fleet problems feed (ut-docs#5446)

Date: 2026-11-04 · Lane: lane:cloud-54 · Author: Dev subagent (Sonnet) · Reviewer: independent subagent (Opus)

## What shipped

- `internal/androidupdate` (new): `ReportRefused(class)` raises one keyed WARN
  problem `androidupdate.refused` (`ProblemKeyRefused`) naming a reason class
  from a closed set of 7 (`unreadable_apk`, `wrong_package`, `not_newer`,
  `signer_unreadable`, `multiple_signers`, `signer_not_pinned`,
  `check_error`). Unknown input becomes `check_error` and is never echoed. The
  same class again logs INFO (no ring flooding); a different class resolves the
  old problem and raises a new one. `ReportAccepted()` resolves it. Mutex-guarded.
- `mobile/mobile.go`: gomobile exports `ReportAndroidUpdateRefused(string)` and
  `ReportAndroidUpdateAccepted()` (strings/no args — gobind-safe), shaped like
  `SetKioskPinned`.
- Android shell: `UpdateLogic.RefuseClass` enum; `ApkCheck.Refuse(cls, reason)`.
  `MainActivity.verifyThenInstall` reports only `cls.wire` to Go (never
  `verdict.reason`, which carries digests / foreign package names), inside
  `runCatching`; an accepted APK resolves the problem before the installer opens.
- The existing heartbeat `collectProblems` digest carries it to ut-cloud; admin
  store detail lists it per device. No ut-cloud change.
- Docs: `android/README.md`; ut-docs ADR-0179 out-of-scope note and
  `architecture/packaging.md`.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | Medium (Tester) | `go test ./...` failed: `TestSettingScope_EveryUsedKeyIsClassified` read `androidupdate.ProblemKey` as a settings key. | Fixed — Tester added an exception; Reviewer replaced it with the codebase convention `ProblemKeyRefused` (as `selfupdate.ProblemKeyUpdateFailed`) and removed the exception. |
| 2 | Low (Dev) | Predicted `guard-deadcode-baseline.sh` failure (functions reached only from `mobile`). | Not a problem — the guard passes (`GOTOOLCHAIN=go1.27.1`): `internal/androidupdate` is outside the roots' load graph, like `mobile`. |
| 3 | Accepted | Problem lives only in the in-memory 50-entry ring; app restart drops it until the next refusal. | Same as every keyed problem; out of scope (BA non-goal). |

Checked fine: concurrency (lock order), UI-thread Go calls (cheap, no I/O),
`runCatching` covers `UnsatisfiedLinkError`, one process so `TillService`'s
heartbeat sees the problem, messages 54–97 chars (<200 cut), hostile class
(300 chars, newline, `<script>`, empty) → `check_error`.

## Verification

- TDD re-verified by the Reviewer in a separate worktree: reverting each of
  whitelist, resolve-on-accept, INFO-on-repeat, resolve-before-new-class,
  class set, mobile forwarding made its named test fail; restored → pass.
- Tester: 8 mutants, all killed. Full gate: `gofmt -l .` empty, `go build ./...`,
  `go test ./...` green (after #1), `golangci-lint run ./...` 0 issues,
  `guard-gobind-skip.sh` pass, 140/142 `build`-job guard lines pass (the two
  others: no shellcheck binary here; `retry-with-backoff.sh` is a wrapper).
- Not run here: Kotlin compile + `UpdateLogicTest` (no Android SDK) — proven by
  android-ci on the PR; driven emulator run with a wrong `-PutApkSignerPins`
  (AC8) — no emulator in a cloud lane.

Pre-flight: Till user-visible behaviour — n/a (no screen, help or string change; problem line is English log text); `en.json` keys — n/a (none); README — n/a (plan/platform claims unchanged); ADR — updated (ADR-0179 out-of-scope note, no decision change; index unchanged); reference/ pages — n/a (none lists problem keys); architecture/packaging.md — updated; android/README.md — updated; follow-up — emulator check stays with the card's AC8 note.

## Verdict

Approve, merge once android-ci is green on the PR.
