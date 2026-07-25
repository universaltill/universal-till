# 013 — Android/iOS shared core

Decision record: `ut-docs/adr/0023-android-ios-till-strategy.md` (read
that first — this doc is the implementation breakdown, not the "why").

## Goal

Run the exact same Go core on Android/iOS that already runs on desktop,
via a thin native shell — no server rewrite, no second plugin host, no
forked business logic.

## Phases (build and ship in this order)

### Phase 1 — Go-side groundwork (in-process boot + gomobile-bind surface)

Done first, and independent of having any mobile SDK installed at all —
everything here is `go build`/`go test`-verifiable today.

- Extracted `main.go`'s full boot sequence (config → DB/migrations →
  plugin host → marketplace enrolment → pages/mux → background jobs →
  HTTP server) into `internal/app.Run(ctx) error` — previously only
  expressed inline in `main()`, un-callable from anywhere else.
  `main.go` now just calls it and `log.Fatalf`s on a non-nil error,
  preserving the CLI's fatal-on-error behavior. Verified with a real
  built binary (boots, `/healthz` 200, identical to pre-refactor) and
  the full existing test suite green. **One real, deliberate behavior
  difference, caught by independent review and worth stating rather
  than glossing over as "purely mechanical"**: the original inline code
  called `log.Fatalf` (→ `os.Exit(1)`, skipping every deferred function)
  at each fatal step; `Run` returns the error instead, so `defer
  database.Close()` now genuinely executes on every one of those same
  failure paths before `main.go`'s own `log.Fatalf` runs. Checked every
  step for a dependency on the old abrupt-exit semantics — found none;
  this is a strict improvement (clean SQLite shutdown even on a fatal
  boot failure), not a regression, but it IS a real behavior change on
  exactly the axis worth double-checking in a refactor like this.
- New `mobile` package (`mobile/mobile.go`) — the actual gomobile-bind
  entry point. Three exported functions only (`Start(dataDir string)
  (string, error)`, `Stop()`, `IsRunning() bool`), deliberately minimal
  because gomobile bind's cross-language boundary only supports a narrow
  set of types (strings/ints/bools/`[]byte`/error — no generics, no
  complex struct fields crossing directly).
  - `Start` runs `internal/app.Run` **in-process** (not a spawned
    sibling binary — mobile apps can't do that the way
    `cmd/unitill-desktop`'s WebView shell spawns `unitill-pos` as a
    child process), on a free loopback port, polls `/healthz` until
    ready (same "start, wait, then show a window" shape as
    `desktop.go`), and returns the address for the native shell's
    WebView to load.
  - `Stop` cancels the context `internal/server.Start` already watches
    for graceful shutdown — no new shutdown mechanism, reuses exactly
    what SIGINT/SIGTERM already trigger on desktop/CLI — and **blocks
    until teardown has actually finished** (including the deferred
    `database.Close()`), not just until the cancel signal was sent.
    First draft returned immediately after sending the signal;
    independent review flagged the real race that leaves — a native
    shell that `Stop()`s then quickly `Start()`s again against the SAME
    on-device data dir (backgrounded-then-foregrounded is a plausible
    real sequence) could race the old instance's in-flight
    `database.Close()` against the new instance's `db.Open()` on the
    same SQLite file. Fixed by having `Stop()` wait on a `done` channel
    the server goroutine closes when `app.Run` truly returns.
  - Idempotent `Start` while genuinely running (same `dataDir`, returns
    the existing address) and a safe no-op `Stop` when nothing's
    running, since a mobile app's lifecycle can call these more than
    once (e.g. iOS foreground/background transitions). A second `Start`
    with a DIFFERENT `dataDir` while running is an explicit error, not
    silently ignored. And — the other real gap independent review
    found — if the server died on its own (e.g. a listener error) with
    Stop never called, `Start`'s "already running" fast-path no longer
    lies: it notices the dead instance via a closed `done` channel and
    starts fresh instead of returning a stale, dead address. The lock
    guarding all of this is held only for brief state transitions, never
    across the actual blocking start/stop work — first draft held it
    across the whole ~10s ready-wait, which review correctly flagged as
    able to block a concurrent `Stop()`/`IsRunning()` call for the full
    timeout.
  - Tests genuinely boot a real server through this exact API (not
    mocked): confirm `/healthz` answers after `Start`, confirm the port
    stops answering after `Stop`, confirm idempotency, confirm `Stop`
    is safe when never started.

### Phase 2 — Android native shell (blocked on tooling)

Not started. Needs the Android SDK + NDK (confirmed absent in the AI
dev environment: `gomobile bind -target=android` fails immediately with
"could not locate Android SDK" — a real, verified blocker, not assumed)
and a decision on packaging shape (foreground service + WebView
`Activity`, per ADR-0023 §1). Once the SDK exists: `gomobile bind
-target=android ./mobile` produces the `.aar` a minimal Kotlin/Java
shell links against.

### Phase 3 — iOS native shell (blocked on tooling + accounts)

Not started. Needs Xcode with the iOS SDK (only the Xcode Command Line
Tools stub is present today, confirmed) and Farshid's own Apple
Developer Program enrolment for real signing/distribution (ADR-0023).
Once available: `gomobile bind -target=ios ./mobile` produces the
`.xcframework` a minimal SwiftUI/UIKit shell links against, hosting an
in-process `WKWebView`.

## Explicitly out of scope for this spec

- The native Kotlin/Swift shell code itself (Phases 2/3) — not
  attempted without the ability to build/verify it; hand-written,
  untested native code would be worse than no code.
- Mobile hardware adapters (printer/drawer/scanner/card reader) — a
  separate, later item per ADR-0023 §2, only needed once a real device
  target exists to build against.
- Installing the Android SDK/NDK or Xcode on Farshid's machine
  unprompted — a multi-GB, environment-modifying step, held for an
  explicit go-ahead rather than done as a side effect of this phase.
