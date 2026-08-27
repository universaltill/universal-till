# shellAppliesWindowMode: gate the platform-dependent assertion by build (ut-docs#1135)

**Card:** universaltill/ut-docs#1135

**Complexity:** easy. Review at Sonnet (fresh context, isolated worktree, no
prior exposure to the implementation reasoning).

## What shipped

`cmd/unitill-desktop/shell_poll_test.go`'s `TestShellAppliesWindowModeGatesTheAdvertise`
used to assert `shellAppliesWindowMode == false` unconditionally. That's only
true in a non-`desktop&&linux` build: `window_mode_linux.go`'s `init()` sets
it `true` under `desktop && linux` (the only platform with a real
`applyWindowMode` today — macOS has none yet, ut-docs#609; Windows's is an
empty stub, ut-docs#610). Since the test file itself carries no build
constraint, `go test -tags desktop ./cmd/unitill-desktop` could never pass on
Linux — the exact case where the variable is `true`.

Fix: the platform-dependent half of the assertion moved into a
build-tag-selected helper, `assertShellAppliesWindowModeForBuild(t)`, called
from the still-untagged test:

- `window_mode_gate_linux_test.go` (`//go:build desktop && linux`) — asserts
  `shellAppliesWindowMode == true`.
- `window_mode_gate_other_test.go` (`//go:build !(desktop && linux)`) —
  asserts `shellAppliesWindowMode == false`.

The rest of `TestShellAppliesWindowModeGatesTheAdvertise` (the HTTP
round-trip against `fetchShellPrefsWithControl`) and all 7 other
`watchShellMode` tests in the file are untouched.

## Independent review (Sonnet, fresh context, isolated worktree)

Verdict: **safe to merge**, no blocking findings.

- **Build-tag partition verified exhaustive and non-overlapping.** The two
  new files' constraints (`desktop && linux` / `!(desktop && linux)`) are
  exact logical complements — every build configuration matches exactly one
  of them, so there is no compile-time collision on the shared
  `assertShellAppliesWindowModeForBuild` name and no build config where the
  helper is undefined. Checked concretely: no tags at all → `other`;
  `-tags desktop` on linux → `linux`; `-tags desktop` on windows → `other`
  (matches `window_mode_windows.go`'s no-op stub, which never sets the
  var); a hypothetical `-tags desktop` on darwin would also hit `other` for
  this helper specifically, though that config doesn't compile end-to-end
  today for an unrelated, already-known reason (no `window_mode_darwin.go`
  defining `applyWindowMode` at all — ut-docs#609, explicitly out of scope
  here).
- **Precedent match confirmed, with one deliberate and correct divergence
  from `autostart_linux.go`/`autostart_other.go`.** The *file-splitting
  pattern* (one file per Linux-specific implementation, one catch-all file
  for the rest) matches. The actual tag expressions differ on purpose:
  `autostart_other.go` is tagged `desktop && !linux` because `autostart.go`
  (the pure logic) is untagged but `reconcileAutostart` itself is declared
  *only* by the two OS-specific files and is called only from
  `desktop.go` (`//go:build desktop`) — so it only ever needs to exist
  under `-tags desktop`. `shellAppliesWindowMode` and the test that reads
  it live in fully untagged files (`window_mode.go`, `shell_poll_test.go`),
  so the helper must also compile in the plain `go test ./...` config with
  no tags at all — `desktop && !linux` would leave that config with no
  matching file and a compile error. `!(desktop && linux)` is the correct,
  necessary choice here; verified by tracing every call site of both
  `reconcileAutostart` and `shellAppliesWindowMode`/`applyWindowMode`
  across the package (`desktop.go`, `webview_fallback.go`,
  `window_mode.go`, `window_mode_linux.go`, `window_mode_windows.go`) to
  confirm the scoping claim rather than taking the commit message's framing
  at face value.
- **No file I/O, no `os.MkdirAll`/cwd-path bug class applicable** — pure
  build-tag/test-only change, confirmed by re-reading all three touched
  files in full.
- **No scope creep, no real client/shop names, no secret-shaped literals**
  in the diff (`git show` + targeted grep across the three files).
- **`t.Helper()` present in both new helpers** — failure line numbers will
  correctly point at the caller in `shell_poll_test.go`, not the helper
  itself.

## Verification beyond the existing tests (independent mutation testing)

Performed personally, from a cold read of the diff, in this isolated
worktree — not trusted secondhand:

1. Temporarily reverted `window_mode_linux.go`'s `init()` to a no-op.
   `go test -tags desktop ./cmd/unitill-desktop/... -run
   TestShellAppliesWindowModeGatesTheAdvertise` **FAILED** as expected:
   `shellAppliesWindowMode = false in a desktop&&linux build —
   window_mode_linux.go's init must set it true`. Restored the file,
   re-ran — **PASSED**.
2. Temporarily flipped `window_mode.go`'s default to
   `var shellAppliesWindowMode = true`. The untagged `go test
   ./cmd/unitill-desktop/... -run TestShellAppliesWindowModeGatesTheAdvertise`
   **FAILED** as expected: `shellAppliesWindowMode = true in a
   non-desktop-linux build — only desktop&&linux may set it`. Restored the
   file, re-ran — **PASSED**.
3. `git status --porcelain` confirmed clean (no leftover diff) after both
   restores.

This independently confirms the fix actually closes the bug in both
directions, not just that the new code happens to compile.

## Gate results (all run in this worktree; cgo deps present —
`pkg-config --exists gtk+-3.0` and `webkit2gtk-4.1` both succeeded)

- `go build ./...` — clean.
- `go build -tags desktop ./...` — clean.
- `go vet ./...` — clean.
- `go test ./cmd/unitill-desktop/...` (no tags) — PASS.
- `go test -tags desktop ./cmd/unitill-desktop/...` — PASS (this is the
  exact command that was broken before the fix; confirmed it now passes on
  Linux, the case ut-docs#1135 describes as previously unable to pass).
- `go test -race ./cmd/unitill-desktop/...` (no tags) — PASS.
- `go test -race -tags desktop ./cmd/unitill-desktop/...` — PASS (2.48s).
- `gofmt -l` on the 3 touched files — no output (clean).

## Not verified / accepted gap

- No darwin build attempted — `-tags desktop` on macOS doesn't compile
  today regardless of this diff (`applyWindowMode` has no darwin
  implementation, ut-docs#609). Unrelated pre-existing gap, out of scope
  for ut-docs#1135 and unaffected by this change.
- No live/manual run of the desktop shell — this is a test-only change
  with no behavioral or runtime code path touched (confirmed by diff scope:
  only test files, and only a test's own assertion — no non-test file
  changed).

## Safe-to-merge verdict

Yes. No fixes requested by review; nothing to apply.
