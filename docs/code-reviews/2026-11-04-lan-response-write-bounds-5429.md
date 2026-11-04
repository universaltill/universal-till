# Review: response-write bounds on the till's listeners (ut-docs#5429)

Date: 2026-11-04 · Branch: `fix/5429-bound-response-writes` · Built by Sonnet,
reviewed independently by Opus 5.5 · Tester: separate agent, run in parallel.

## What shipped

- `internal/httpserver/writes.go`: `New` now wraps every handler (main and
  recovery servers, plain and LAN TLS) in `boundWrites`, outermost, around
  #5338's `boundBodies`. Before each `Write`, `WriteHeader`, `Flush`/`FlushError`
  and `ReadFrom` slice, it sets the write deadline to now + `WriteGrace`
  (15 s) + bytes/`WriteMinRate` (8 KiB/s). The deadline is fresh each time, so
  it never accumulates. A peer that stops reading is cut, and a steady reader
  at or above the rate gets any size.
- Nothing is armed at handler entry, so the silent 25 s shell long-poll and
  slow-computing handlers are untouched. SSE needs no route list, because each
  event re-arms before it writes.
- Hijack: the flag is set before delegating. net/http clears both deadlines,
  and the wrapper never sets one again, so websockets keep their own timeouts.
- Handler exit re-arms the grace once, so net/http's final flush of the
  buffered tail and chunk trailer is bounded. net/http then zeroes the
  deadline for the next keep-alive request.
- `ReadFrom`: for an `*os.File` source (`ServeFile`/`ServeContent` via
  `io.CopyN`), it feeds net/http 256 KiB slices behind a single
  `io.LimitedReader`, so plain-HTTP sendfile is kept. Any other source is
  copied through `Write`, so a slow upstream is never charged to the client.
- Fail open (`httptest.ResponseRecorder`, no `SetWriteDeadline`).
  `bodyDeadline` is renamed to `rateDeadline` (shared overflow-safe
  arithmetic), and `newWith` takes a `limits` struct.
- `pages/recovery.go`: `recoveryResponseWriter.Unwrap`, so
  `ResponseController` (`FlushError`, deadlines) reaches through it. This is
  only needed for RC users.
- `pages/window_state_api.go`: the long-poll safety comment now names the
  write bound.

## Findings

| id | sev | finding | outcome |
|---|---|---|---|
| W1 | major | One large `Write` (whole rendered pages via `w.Write(buf.Bytes())`, `ai_cutout` PNGs, `data_api`/`eod_api` raw exports) armed grace + len/rate for the whole buffer. A stalled peer on a 32 MiB write held the goroutine for 15 s + 4096 s, and a 10 MB export for about 21 min. That is bounded, but not the ~47 s the design states for files. | fixed: `Write` feeds `chunk`-sized slices, each with its own deadline. Red→green `TestSingleLargeWriteNonReaderCut` (red: "handler still blocked after 10s") |
| W2 | major | `response.Hijack` calls `w.cw.flush()` (the pending header and conn buffer) *before* `hijackLocked` clears the deadlines. Scenario: a handler writes its 101, works for longer than the grace, then hijacks. The flush fails under the stale deadline, `bufw.Reset` drops the header, and the client gets raw bytes with no 101, so the upgrade breaks. | fixed: `Hijack` arms the grace first (safe, because net/http clears it right after). Red→green `TestHijackAfterIdleFlushesHeader` (red: got `"world"`, no `HTTP/1.1 101`). `TestNoRearmAfterHijack` now expects 2 arms (Write and pre-hijack), still none at exit |
| W3 | — | Is "flag before delegating" right, or should the flag be set only on success? | kept as built: `hijackLocked` can return an error (`Peek` failure) *after* `hijackedv=true` and the deadline clear, so flagging only on success could put a deadline on a hijacked conn. A failed hijack leaves the rest of that response unbounded, which is acceptable (a real `*response` cannot fail it except in that case). Doc comment corrected |
| W4 | nit | `limits.writeChnk` abbreviation; the `writeChunk` comment said "sendfile slice" only | fixed: renamed `writeChunk`; comment covers Write slices |
| W5 | minor | Docs: ut-docs `architecture/pos-auth.md` on `origin/main` has the #5338 "Request-body bounds" subsection, but the architect grepped a stale local checkout and found no page, so the write bound is undocumented there | deferred to the orchestrator, **before merge**: a ready-to-paste "Response-write bounds (ut-docs#5429)" subsection is in the session scratchpad (`5429-pos-auth-section.md`). This is a separate ut-docs PR |
| W6 | low | Cost: one `SetWriteDeadline` (a runtime-poller timer update, no syscall; `tls.Conn` forwards it) per `Write`. `html/template` issues many small writes per page | accepted: this is nanoseconds against a bufio write. If profiling ever shows it, skip a re-arm while the current deadline still covers grace/2 + n/rate |
| W7 | low | Test quality: `TestServeFileNonReaderCut` would pass with no per-slice arm, because the `WriteHeader` arm still bounds it as a fixed deadline | accepted: `TestReadFromKeepsLimitedReaderSingleLayer` asserts ≥ 3 arms and one `LimitedReader` layer over the `*os.File`, and `TestServeFileReaderCompletes` covers the opposite failure (a fixed deadline cutting a reader) |
| W8 | — | Concurrency: `hijacked`/`disabled` are unsynchronised | accepted: `ResponseWriter` is single-goroutine by contract, and Hijack and the exit defer run on the handler goroutine. No SSE handler here writes from a second goroutine (the only `go func` among the streaming pages, `eod_api.go:977`, does not write the response). `-race` is clean |

Checked and found correct: `rateDeadline` clamps rate ≤ 0 and huge n
(`TestRateBodyDeadlineArithmetic`). An expired deadline during an idle SSE gap
fails nothing, because the next write re-arms before touching the conn, and
`tls.Conn`'s sticky write error arises only from a write that actually timed
out. After a cut, `checkConnErrorWriter` cancels `r.Context()`, so SSE loops
that ignore write errors still exit. `ResponseController.Hijack` finds bw's own
`Hijack` before any `Unwrap`, as do `httputil.ReverseProxy` upgrades and
coder/websocket. `lantls` is HTTP/1.1 only, so there is no h2 question.
`limited` with N ≤ 0 copies 0 bytes. `peekedConn` forwards `ReadFrom`, so
sendfile survives. No `CloseNotify`, `SetWriteDeadline` or `Pusher` users exist
in the repo. The only other `http.Server` is the loopback
`cmd/unitill-desktop/control.go`.

## Verification beyond unit tests

- Reviewer re-ran the Dev's TDD claims as mutations, then restored. Without
  the exit re-arm, `TestHandlerReturnsAfterLongComputeFlushes` and
  `TestKeepAliveSecondRequestNotCut` fail. Allowing a re-arm after hijack makes
  `TestHijackedConnNotCut` and `TestNoRearmAfterHijack` fail. Without the
  per-Write/slice arm, `TestNonReaderCutWithinBound` and
  `TestWriteBoundOverTLS` fail.
- W1 and W2 tests were written first and seen red on the unfixed code, then
  green.
- Gates (reviewer run): `gofmt -l .` clean; `go vet ./internal/httpserver/
  ./internal/pages/`; `go build ./...`; `golangci-lint run
  ./internal/httpserver/...` 0 issues; `go test -race -count=5
  ./internal/httpserver/` green (155 s). In `internal/pages`, the
  recovery/SSE/stream/window-state/pairing tests passed under `-race`. The
  full `pages` package under `-race` had not finished after about 45 min on
  the shared CPU (0 races reported so far), as with #5338, so CI is the
  authority for it.

Pre-flight: till user-visible behaviour: n/a (no UI and no help topic; these
are LAN transport bounds only); reference/architecture: deferred to the
orchestrator before merge (W5, `architecture/pos-auth.md` subsection
drafted); en.json keys: n/a (none); README: n/a (no text about listener
timeouts); ADR: n/a (the write-side twin of #5338, no new mechanism);
follow-ups: none new.

## Verdict

Approve once W5 lands. W1, W2 and W4 are fixed; W3 and W6–W8 are accepted.
