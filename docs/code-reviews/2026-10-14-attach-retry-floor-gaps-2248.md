# Code review — attach-retry floor: residual gaps (ut-docs#2248)

Date: 2026-10-14 · Branch: `fix/2248-attach-retry-floor-gaps` · Lane: `lane:cloud-54`
Built by Sonnet (easy card); reviewed by Opus 5.5 in a fresh context.

## What shipped

`cmd/unitill-desktop` closes the three gaps the #1278 review left (N4/N5/N6):

1. **Operator override.** `UT_SHELL_ATTACH_RETRY_SECONDS` (default 15s, `0`
   disables the floor, malformed/negative/>600 falls back with a stderr
   line). Parsing is shared with `UT_SHELL_MIN_UPTIME_SECONDS` via
   `secondsFromEnv`; `gateDuration` behaviour and wording are unchanged.
2. **Unreadable `/proc/uptime`** no longer collapses `attachDeadline` to a
   single immediate probe: it gets the floor from launch, like the
   gate-disabled case.
3. **Small non-zero gate** (e.g. `=1`): with uptime known and the gate
   active the window is `holdFor(up, max(gate, floor))` — the floor is
   measured from boot, so a cold boot at 7s still retries 8s while a
   warm/manual launch past both costs one probe. Default 60s gate unchanged.

`attachRetryDuration(up, upKnown, min, floor)` stays pure in the untagged
file so CI's plain `go test` covers it. README and doc comments updated.

## Findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | minor | `attachDecisionLine` doc said a disabled/unreadable gate means no retry window — now false | fixed |
| 2 | minor | `waitForAttach` doc, `desktop.go`, README still said the windows always coincide / omitted unreadable uptime | fixed |
| 3 | minor | spawn-path delay now also applies to disabled gate / unreadable uptime / small gate — undocumented | fixed (code comment + README) |
| 4 | nit | one over-long reflowed comment line | fixed |
| 5 | nit | env test `os.Unsetenv` without restore | accepted — copies the existing `TestGateDurationHonoursEnv` pattern |
| 6 | nit | the `err == nil` wiring in the desktop&&linux file is vetted, not tested | accepted — the existing tested-pure/untested-wiring split |
| 7 | nit | fallback stderr messages unasserted | accepted — same as the old knob; shared helper prevents drift |

No blocker or major. Verdict: safe to merge.

## Verified

- `gofmt -l`, `go build ./...`, `go vet ./cmd/unitill-desktop/`,
  `go test ./cmd/unitill-desktop/`, `golangci-lint run ./...` (0 issues),
  guards i18n / data-access / core-neutral / compliance-claims: clean.
- Tagged Linux file type-checked: `go vet -tags desktop` on
  `attach_gate.go attach_gate_linux.go startup_gate.go startup_gate_linux.go`
  (the full package needs GTK headers the container lacks; CI's
  desktop-shell job builds it).
- TDD: with the source reverted the tests fail to build (signature change),
  so the reviewer also ran behavioural mutations — `max(min,floor)`→`min`
  (gap 3), dropping `|| !upKnown` (gap 2), an absolute floor with the gate
  active, and an env-ignoring floor — each fails the new tests.
- Not driven on hardware (Pi cold boot); the decision logic is pure and
  table-tested.
