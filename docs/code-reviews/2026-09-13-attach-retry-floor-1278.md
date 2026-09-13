# Review: attach-retry floor independent of the render-defect gate (ut-docs#1278)

## What shipped

`cmd/unitill-desktop/attach_gate.go` gained `attachRetryFloor` (15s) and a
new pure function `attachRetryDuration(up, min time.Duration) time.Duration`
— when the render-defect startup gate is disabled
(`UT_SHELL_MIN_UPTIME_SECONDS=0`, `min == 0`), it returns the floor instead
of `holdFor(up, min)`, so the attach-probe retry (ut-docs#1199) keeps its
own ~15s window instead of collapsing to a single immediate probe. When the
gate is active, behaviour is byte-for-byte unchanged: `attachRetryDuration`
still just delegates to `holdFor(up, min)`.

`attachDeadline()` (`attach_gate_linux.go`) now calls `attachRetryDuration`
instead of inlining the old `if min == 0 { return time.Now() }` check, and
skips reading `/proc/uptime` entirely in the disabled-gate case (the floor
doesn't need it), same as the original fast path.

The new logic deliberately lives in the **untagged** `attach_gate.go`, not
the `desktop && linux`-tagged `attach_gate_linux.go`: CI's `desktop-shell`
job only builds/vets the tagged file and never runs `go test` on it — the
untagged file is what CI's plain `go test $(go list ./...)` step actually
executes, so this is where the fix needed to live to get real, executing
test coverage.

New tests in `attach_gate_test.go`:
`TestAttachRetryDurationFloorsWhenGateDisabled` (the actual fix) and
`TestAttachRetryDurationUnchangedWhenGateActive` (pins the active-gate
behaviour in both directions, including the near-end-of-window case where
the remaining window is smaller than the floor — this must NOT be raised to
the floor).

## What the independent review found

Opus subagent, isolated worktree (`.claude/worktrees/review-1278`), ran the
full change independently — initial verdict **FAIL** on two blocking
documentation-staleness findings; both fixed in this same PR, verdict now
PASS:

- `go build`/`go vet` (untagged), `gofmt -l`, `go test ./cmd/unitill-desktop/...`
  — all clean.
- `CGO_ENABLED=1 go build/vet -tags desktop`, `golangci-lint --config=
  .golangci-desktop.yml --build-tags=desktop` (CI-pinned v2.5.0), and
  `scripts/ci/guard-deadcode-baseline.sh` — all clean (GTK/WebKit dev
  headers were actually present in the review environment, so this wasn't
  skipped).
- **Independently re-verified TDD**, in the isolated worktree, two ways:
  (1) reverted the `min == 0` branch to `return 0` — the new floor test
  failed for real (`= 0s, want attachRetryFloor (15s)`), not a crash/error;
  restored, re-passed. (2) applied the floor unconditionally (including
  when the gate is active) — the active-gate test failed for real on all
  three cases, confirming AC3 (unchanged-when-active) is genuinely pinned
  in both directions, not just the disabled-gate direction.
- Confirmed in `.github/workflows/ci.yml` directly that the `build` job's
  test step runs without `-tags desktop` and the `desktop-shell` job never
  runs `go test` — the new tests' placement in the untagged file is what
  makes them actually execute in CI.
- Checked `CLAUDE.md` non-negotiables (repository pattern / money / i18n /
  offline-first / plugin signing) — none apply; confirmed rather than
  assumed (no SQL, no `money.Money`, no locale/UI strings beyond the
  existing stderr diagnostic, no plugin/kiosk surface).
- Checked the two recurring bug classes this pipeline watches for (missing
  `os.MkdirAll` on a file-write handler; a cwd-relative path where
  `paths.Data(...)` belongs) — neither applies, confirmed (no file writes
  in the diff; the only path touched is the pre-existing absolute
  `/proc/uptime` const).
- Checked `web/help/` for any operator-facing doc needing an update — none
  found; this is internal boot-timing behaviour with no documented step or
  UI surface.
- No secrets, no real client/shop names in the diff.

**Blocking findings, both fixed in this PR:**
- `README.md`'s "Attach-vs-spawn cold-boot race" section still asserted,
  verbatim, that `UT_SHELL_MIN_UPTIME_SECONDS=0` disables the retry along
  with the gate — exactly the defect this card just closed. Rewrote both
  that section and the startup-gate section just above it to describe the
  actual (fixed) behaviour, cross-referencing ut-docs#1278.
- `attach_gate.go`'s own pre-existing doc comment on `waitForAttach`
  (written for ut-docs#1199) claimed the disabled-gate case "costs exactly
  the one probe" and "never retries past that window (attachDeadline
  derives from the same gate duration...)" — both now false for the
  disabled-gate case. Rewrote to describe both branches (gate active vs.
  gate disabled) accurately.
- Ride-along: `desktop.go`'s call-site comment had the identical staleness
  one level up ("waitForAttach retries across the same window ut-docs#1093's
  own startup gate already holds the window back for" — no longer
  universally true). Fixed in the same pass.

**Non-blocking, accepted as-is / deferred (ut-docs#2248 filed as a
follow-up):**
- The floor is a compile-time constant with no operator override, despite
  its own comment suggesting it should be "tune[d] to the measured systemd
  unit start time" — there's no knob to tune. Not required by this card's
  acceptance criteria; left as a follow-up rather than adding a new env var
  speculatively.
- An unreadable/unparseable `/proc/uptime` still falls through to a single
  immediate probe rather than the floor — a second, rarer silent path back
  to the pre-fix behavior. Judged low-likelihood on real hardware and out
  of this card's explicit scope (which is about the operator's
  `UT_SHELL_MIN_UPTIME_SECONDS=0` choice, not uptime-read failures).
- `UT_SHELL_MIN_UPTIME_SECONDS=1` (or any small non-zero value) can still
  let `holdFor` return 0 on a slow-enough cold boot, reopening the race the
  card's stated goal cares about — strictly consistent with the acceptance
  criteria as worded (only `min == 0` is in scope), but worth tracking.
- With the gate disabled and no server ever answering (dev launch, tarball
  install, down `.deb` service), launch now silently blocks ~15s before
  spawning (previously instant) — intended and bounded by the card, but
  newly-introduced cost worth noting for anyone tuning boot time.

## Verified beyond automated tests

- Real TDD red→green, independently re-verified by a second, fresh-context
  model instance (Opus) in an isolated worktree, in both directions (floor
  applies when disabled; floor does NOT leak into the active-gate case).
- `golangci-lint` (desktop config) and the whole-program deadcode baseline
  guard both clean under the real `desktop` build tag with actual GTK/
  WebKit dev headers present — not just the untagged build.

## Safe to merge

Yes, after the two blocking doc fixes above (both prose-only, no logic
change — the full gate re-ran clean after applying them).
