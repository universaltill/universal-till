# Code review — desktop shell attach-vs-spawn cold-boot race (ut-docs#1199)

- **Date:** 2026-08-29
- **Repo / branch:** `universal-till`, `fix/1199-desktop-shell-attach-race-startup-gate`
- **Reviewer:** independent reviewer subagent, fresh context, different model
  from the implementer (Opus — this pipeline's non-`easy` review tier)
- **Verdict: SAFE TO MERGE.** No blocking or major findings. The reviewer made
  **one comment/README wording change only** (Finding 1) — no behavioural code
  was touched. Three low-severity items recorded, none blocking; two suggested
  as follow-up cards.

## What shipped

`cmd/unitill-desktop/desktop.go`'s `main()` decided attach-vs-spawn from a
**single** `tillAlreadyRunning("127.0.0.1:8080")` probe (a `/healthz` GET on a
1.5s-timeout client). On a cold `.deb`/systemd boot the shell's own process
starts well before the systemd-managed `unitill-pos` service finishes binding
`:8080`, so that one probe loses the race every time and the shell spawns a
**second** server as the desktop user. Consequences confirmed on real hardware
in the issue: the on-screen till trades against the desktop user's own SQLite
file rather than the service's (split-brain data), and in-app update reports
"unsupported" because the process actually serving the UI cannot write the
service's install directory.

The fix turns that single probe into a bounded retry, reusing ut-docs#1093's
existing startup-gate window rather than inventing a second notion of "how far
into boot are we":

| File | Build tag | What |
|---|---|---|
| `attach_gate.go` (new) | *(none)* | `waitForAttach(deadline, probe, sleep, now)` — retries `probe` every `attachPollInterval` (500ms) until it succeeds or `now()` is no longer before `deadline`. Clock/sleep injected, so it is testable with no wall-clock cost. |
| `attach_gate_linux.go` (new) | `desktop && linux` | `attachDeadline()` = `time.Now().Add(holdFor(readUptimeFrom(procUptime), gateDuration()))`; `time.Now()` (i.e. decide immediately) when the gate is disabled or `/proc/uptime` is unreadable. |
| `attach_gate_other.go` (new) | `!(desktop && linux)` | `attachDeadline()` → `time.Now()`; single probe, unchanged behaviour. |
| `attach_gate_test.go` (new) | *(none)* | 4 unit tests over a `fakeClock` whose `Sleep` advances the clock instead of blocking. |
| `desktop.go` (modified) | `desktop` | The probe call site becomes `waitForAttach(attachDeadline(), func() bool { return tillAlreadyRunning("127.0.0.1:8080") }, time.Sleep, time.Now)`. |
| `README.md` (modified) | — | New "Attach-vs-spawn cold-boot race" section + manual recovery guidance for an install already split-brained by the pre-fix race. |

No SQL, no user-facing strings, no money, no routes, no templates — the
repository-pattern, i18n, RTL, money and help-topic rules are genuinely N/A
here (confirmed by running their guards, not assumed; see below).

## Verification performed

Everything in this table was run by the reviewer in this session. Nothing is
carried over from the implementer's report.

| Check | Result |
|---|---|
| `gofmt -l .` | empty |
| `go vet ./...` | clean |
| `go build ./...` | pass |
| `go test ./cmd/unitill-desktop/... -run TestWaitForAttach -v` | 4/4 pass |
| `go test ./cmd/unitill-desktop/... -race -run TestWaitForAttach` | pass (1.03s) |
| `go test ./...` (full suite) | **exit 0**, 41 packages `ok` |
| **All 18 guards declared in `.github/workflows/ci.yml`** (enumerated from the workflow, not from the CLAUDE.md snapshot) | 18/18 PASS |
| Everything above **re-run after** the reviewer's Finding-1 edit | identical results |

### TDD claim — re-verified personally, and it is partly overstated

The claim relayed to review was that *each* new test was confirmed to fail
against a reverted single-probe implementation. The reviewer reverted
`waitForAttach`'s body to `return probe()` and ran the tests directly
(revert → run → restore executed as one atomic shell invocation, per the
reviewer skill's ut-docs#386 warning; `attach_gate.go` was diffed against a
backup afterwards and confirmed **byte-identical** on restore):

```
=== TEST AGAINST REVERTED (single-probe) IMPLEMENTATION ===
--- PASS: TestWaitForAttachSucceedsOnFirstProbeNeverSleeps
--- PASS: TestWaitForAttachPastDeadlineDecidesFromOneProbe
=== RUN   TestWaitForAttachRetriesUntilProbeSucceeds
    attach_gate_test.go:75: waitForAttach() = false, want true (probe eventually succeeded)
--- FAIL: TestWaitForAttachRetriesUntilProbeSucceeds
=== RUN   TestWaitForAttachGivesUpAtDeadlineNeverAttaches
    attach_gate_test.go:104: probe called 1 times, want exactly 3
--- FAIL: TestWaitForAttachGivesUpAtDeadlineNeverAttaches
FAIL
=== RESTORING ===
RESTORED byte-identical
=== TEST AFTER RESTORE ===  4/4 PASS
```

**2 of 4 fail, not 4 of 4** — and that is correct, not a defect. The two that
fail are the genuine red-green evidence for the behaviour change, and both fail
on an on-topic assertion (wrong return value; probe called once instead of
three times), *not* a compile error. The two that pass against both
implementations are deliberate **characterisation** tests pinning the
degrade-to-single-probe path that must NOT change. They are valuable and
correctly written; they are simply not red-green proof. Recorded as Finding 4
so the record doesn't overclaim. No test change requested.

### The desktop-tagged files CI cannot compile (ut-docs#1071)

`go build -tags desktop ./cmd/unitill-desktop` fails in this container purely on
missing dev libraries — the known, separately-tracked gap:

```
# github.com/webview/webview_go
Package 'gtk+-3.0', required by 'virtual:world', not found
Package 'webkit2gtk-4.1', required by 'virtual:world', not found
```

Rather than hand-reading `attach_gate_linux.go` and the `desktop.go` call site,
the reviewer got a **real compiler** over them: a throwaway scratch module
containing `startup_gate.go`, `startup_gate_linux.go`, `attach_gate.go` and
`attach_gate_linux.go` with their `//go:build` lines stripped, plus a `main`
holding a *verbatim* copy of `tillAlreadyRunning` and of `desktop.go:79`'s
condition expression. `go vet` and `go build` on that module both pass. Every
symbol the new Linux file assumes therefore genuinely exists with the assumed
signature: `gateDuration() time.Duration`, `readUptimeFrom(string) (time.Duration, error)`,
`holdFor(time.Duration, time.Duration) time.Duration`, `procUptime`
(`desktop && linux`, same tag as its caller), and `time.Sleep` /`time.Now`
matching `waitForAttach`'s `func(time.Duration)` / `func() time.Time`
parameters. Shadowing the builtin `min` in `attachDeadline` is legal and is
already the existing convention in `waitForSafeStartup`.

Running that scratch module against real and fixture `/proc/uptime` data
confirms the deadline arithmetic and the degradation paths:

```
gate=0                              -> deadline in 0s      (single probe)
gate=999999 (typo, capped->default) -> deadline in 0s      (host uptime 16m35s)
uptime_cold  uptime=7s              -> attach retry window = 53s
uptime_warm  uptime=1m15s           -> attach retry window = 0s
past-deadline: got=false calls=1                            (want false, 1)
cold boot, service binds at T+12s: attached=true  after  25 probes, 12s simulated
cold boot, NO service:            attached=false after 107 probes, 53s simulated
```

**Build-tag exclusivity checked mechanically**, not by eye — `go list` under
each tag/GOOS combination shows exactly one `attachDeadline` in every build, so
neither "redeclared" nor "undefined" is reachable:

| Build | `attach_gate_*.go` compiled |
|---|---|
| default (no tags) | `attach_gate.go`, `attach_gate_other.go` |
| `-tags desktop` GOOS=linux | `attach_gate.go`, `attach_gate_linux.go` |
| `-tags desktop` GOOS=darwin | `attach_gate.go`, `attach_gate_other.go` |
| `-tags desktop` GOOS=windows | `attach_gate.go`, `attach_gate_other.go` |

The tags are byte-identical to the existing sibling convention
(`startup_gate_linux.go` / `startup_gate_other.go`), and the pairing tracks
`startup_gate_*` exactly in all four columns. `attach_gate_other.go` is
compiled by the default build, so `go build ./...`/`go vet ./...` above already
type-check it for real. `attach_gate_test.go` carries no tag and compiles under
both builds; no symbol it introduces (`fakeClock`, `SleepCounting`,
`attachPollInterval`, the four test names) collides with anything else in the
package.

### Correctness read

- **Boundary is right.** `if !now().Before(deadline)` is checked *after* the
  probe, so a deadline equal to or earlier than now yields exactly one probe
  and zero sleeps — bit-for-bit the behaviour being replaced. Verified by
  `TestWaitForAttachPastDeadlineDecidesFromOneProbe` and by the scratch-module
  run above (`calls=1`). No off-by-one: the give-up test's
  2×`attachPollInterval` budget produces exactly 3 probes / 2 sleeps, and that
  arithmetic was re-derived independently and matches.
- **No new delay on the attach path.** `attachDeadline()` derives from the same
  `gateDuration()`/`holdFor()` as `waitForSafeStartup()`, which is called later
  in `showWindow` (`webview_fallback.go:38`), so when the shell attaches the
  window still opens at the very instant it always did. The retry consumes time
  the gate was already spending. (One case *does* pay — see Finding 1.)
- **Degradation is complete.** Gate disabled (`UT_SHELL_MIN_UPTIME_SECONDS=0`),
  unreadable `/proc/uptime`, warm/manual launch past the gate, and every
  non-`desktop&&linux` build all resolve `attachDeadline()` to `time.Now()` →
  one probe. The unreadable-uptime fallback deliberately mirrors
  `waitForSafeStartup`'s own "can't tell, start immediately" choice.
- **Concurrency: safe.** `waitForAttach` is a single-goroutine loop over
  injected closures, called once from `main()` before any other goroutine
  exists (`runtime.LockOSThread` in `init`, the child not yet spawned). It
  holds no state, shares nothing, and the injected closures in production are
  `time.Sleep`/`time.Now` (goroutine-safe) and a fresh-`http.Client` probe.
  `-race` on the tests is clean.
- **No probe-loop resource leak.** `tillAlreadyRunning` builds a new
  `http.Client` per call but with a nil `Transport`, so it uses the shared
  `http.DefaultTransport` pool; the body is closed via `defer`, and a
  connection-refused probe (the common failure on a not-yet-bound port) opens
  nothing at all. 107 probes over a 53s window is not a hot loop.
- **Scope is clean.** `git status`/`git diff` show only the six files above.
  No stray edits, no real client/shop name used as demo data, no
  secret-shaped literal (the diff introduces no credentials at all).
- **README recovery guidance is accurate** against the code and the issue: two
  `unitill-pos` processes, the desktop-user one on `:8080` with its own
  `~/.local/share/...` DB, the `pos`-user one under `/opt/unitill/data`; no
  automated merge; explicitly framed as a one-off recovery, not a feature.

### Rules-compliance spot-checks (confirmed, not assumed)

- Repository pattern: no SQL added anywhere; `guard-data-access.sh` PASS.
- i18n / RTL: no template, locale or user-facing string touched;
  `guard-i18n.sh` PASS. The two new stderr-adjacent strings in the package are
  developer diagnostics on `os.Stderr`, pre-existing convention for this
  binary, not UI copy.
- Money: none present.
- Compliance wording: `guard-compliance-claims.sh` PASS.
- Manual (`web/help/`): no user-facing page route added or changed, no screen
  altered, so no topic goes stale; `guard-help-topics.sh` (incl. its
  page-route coverage check) PASS. The `cmd/unitill-desktop/README.md` change
  is developer/maintainer documentation, and the recovery procedure it adds is
  a support operation on the filesystem, not something a shop owner performs
  through the UI — it does not belong in the shop-owner manual.
- UX guidelines: N/A, no UI surface in the diff.
- The two recurring bug classes this pipeline keeps finding were checked
  explicitly and are absent: the diff contains no file-write handler (so no
  missing `os.MkdirAll`) and no filesystem path at all beyond the existing
  `procUptime` constant (so no cwd-relative path where `paths.Data(...)`
  belongs).

## Findings and disposition

**1. Low — "costs nothing extra" was overstated for the no-service cold boot.
FIXED BY REVIEWER (comment/README wording only).**
`attach_gate.go`'s doc comment claimed the change adds "no new perceived delay
on top of ut-docs#1093's existing hold", and the README claimed "the retry
costs nothing extra when it fails to attach — the window was never going to
open before the gate elapsed anyway". True on the attach path. **Not true on
the spawn path.** Previously the `unitill-pos` child was started at the very
first probe (≈T+7s) and both its start-up *and* `main()`'s up-to-10s dial-wait
loop (`desktop.go:174-180`) overlapped the gate's hold, so at T+60s the window
opened onto an already-listening server. Now the spawn is serialised *after*
the retry window, so on a cold boot with genuinely nothing on `:8080` (tarball
install with an XDG autostart entry, or a `.deb` whose service is down) the
till appears a few seconds later than it used to — worst case the full dial
loop. This is **inherent to the fix and correct**: spawning speculatively in
parallel with the retry is precisely the two-server split-brain the ticket is
about, so no code change is appropriate. Only the claim needed correcting.
The reviewer rewrote the relevant paragraph in `attach_gate.go`'s doc comment
and the corresponding README paragraph to state the trade-off plainly. Judged
trivial (documentation-only, zero behavioural surface) and therefore fixed
directly rather than handed back; the full gate was re-run afterwards.

**2. Low — the #1199 fix is coupled to the #1093 mitigation's kill switch.
ACCEPTED, documented, follow-up suggested.**
`attachDeadline()` derives its window from `gateDuration()`, so setting
`UT_SHELL_MIN_UPTIME_SECONDS=0` — which an operator may legitimately do for a
completely unrelated reason (X11 rather than Wayland, a non-Pi Linux desktop,
any machine without the WebKitGTK 2.52.6 render defect) — silently also
disables the boot-race retry, returning that machine to the exact single probe
the ticket says loses the race every time. The two defects are independent;
one env var switching off both is a footgun. Accepted for this change: the
alternative (an independent attach window) adds real delay to a genuine
no-service launch, and reusing the gate is what makes this fix free on the
attach path. Mitigated for now by making it **explicit** — the reviewer's
Finding-1 README edit adds a bolded note stating that `=0` disables both.
Suggested follow-up card: an independent short attach floor (~10-15s) that
applies even when the render gate is off.

**3. Low — the retry is silent, in exactly the window this bug class lives in.
NOT FIXED, follow-up suggested.**
`waitForSafeStartup` logs `startup gate: Ns into boot, holding Ns before
opening the window (ut-docs#1093)`. `waitForAttach` logs nothing, so a field
engineer reading journal output sees up to ~53s of silence before the shell
either attaches or spawns — and the original defect took live hardware
investigation precisely because this decision is invisible. One `os.Stderr`
line from `attachDeadline()` when it returns a non-zero window (mirroring
`waitForSafeStartup`'s existing message, and reporting which branch was
finally taken) would make the next occurrence readable from a log alone.
Deliberately **not** fixed by the reviewer: it touches the production
decision path inside a file this container cannot compile normally (see
ut-docs#1071), so it is the implementer's change to make, not a reviewer
drive-by. Non-blocking. Suggested follow-up card.

**4. Nit — TDD claim precision. NO CHANGE NEEDED.**
Only 2 of the 4 new tests fail against a reverted implementation; the other
two are characterisation tests for the deliberately-unchanged single-probe
path. See the verification section above. The tests themselves are
well-designed (deterministic fake clock, zero wall-clock cost, assertions on
*both* the return value and the sleep/probe counts, which is what makes
"never adds a delay to an ordinary launch" actually testable). Recorded only
so the review record states the evidence accurately.

## Explicitly deferred

- An attach retry window independent of `UT_SHELL_MIN_UPTIME_SECONDS`
  (Finding 2) — backlog card.
- A stderr line covering the attach-vs-spawn decision (Finding 3) — backlog
  card.
- Per-PR CI compilation of the Linux desktop shell remains unavailable
  (ut-docs#1071, pre-existing). Worked around for this review by the
  scratch-module type-check described above, which is genuine compiler
  verification of `attach_gate_linux.go` and of `desktop.go`'s new call-site
  expression — but it is not a substitute for building the real tagged binary,
  and the CGO window code in `desktop.go` around the change remains
  uncompiled here.

## Safe-to-merge verdict

**Yes.** The logic is correct at its boundaries, degrades exactly to the
previous single-probe behaviour everywhere it should, is verified by real
red-green tests plus a real compiler over the files CI cannot build, and
leaves the full suite and all 18 CI guards green. The only reviewer edit was
documentation wording. Nothing found here needs a second review round.

### Mid-review forced commit — checked, and it is clean

A commit (`ee308c6`, "fix(desktop): retry the attach probe across the startup
gate window (ut-docs#1199)") landed on the branch *during* this review, not
made by the reviewer — the stop-hook-forced-commit situation ut-docs#386
warns about. It was inspected rather than assumed safe:

- **The committed `waitForAttach` body is the real retry loop, not the
  temporarily-reverted `return probe()`.** The TDD revert/restore was run as a
  single atomic shell invocation precisely so no turn boundary could fall
  between them, and `git show HEAD:cmd/unitill-desktop/attach_gate.go`
  confirms it worked.
- **Author is correct:** `Farshid Mirza
  <4035824+farshidmirza@users.noreply.github.com>` — the pipeline owner's
  GitHub-linked noreply identity, not an AI-tool default. `git config
  user.name`/`user.email` in this checkout match.
- Contents are exactly the six intended files, nothing stray.
- The branch has **no upstream and has not been pushed**.

**Working-tree note for the orchestrator:** the reviewer modified
`cmd/unitill-desktop/attach_gate.go` (doc comment only) and
`cmd/unitill-desktop/README.md` (one paragraph) under Finding 1 — re-check
`git diff` before committing. No other file was touched by the reviewer, and
`attach_gate.go`'s executable body is byte-identical to the state handed to
review (verified by backup diff after the TDD revert/restore).
