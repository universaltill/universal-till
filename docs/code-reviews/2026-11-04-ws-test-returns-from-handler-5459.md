# 2026-11-04 — websocket write-bound test returns from the handler (ut-docs#5459)

**What shipped.** Test-only. `TestWebsocketOutlivesGrace`
(`internal/httpserver/writes_test.go`) used to keep the whole websocket
exchange inside the handler, so it still passed when `boundWrites` never set
its `hijacked` flag (found by mutation in the #5429 Tester pass). It now
accepts the upgrade, hands the conn to a goroutine and returns from the
handler, like a real hub. The goroutine sleeps 900 ms (3x the 300 ms test
grace) and writes 1 MiB. Its result goes to a buffered `serverDone` channel
that the test waits on, so no goroutine or `t.*` call outlives the test.
`writes.go` is unchanged.

**Pre-flight:** web/help topic: n/a (test only). `make docs-shots`: n/a.
reference/ page: n/a. README: n/a. en.json keys: n/a. Lang packs: n/a.
ADR: n/a. Review record: this file. Follow-ups: none.

**Review** (independent subagent, Opus 5.5; Dev was Sonnet), run in a
throwaway worktree of the WIP commit:

- `gofmt -l internal/httpserver/` clean; `go vet` OK;
  `go test ./internal/httpserver/ -race -count=1` ok;
  `-run TestWebsocketOutlivesGrace -count=20 -race` ok, no flakes.
- Mutation (delete `b.hijacked = true`, `writes.go:121`): the new test fails
  3/3 (`len=0 err=failed to get reader: failed to read frame header: EOF`);
  the old test passes 3/3 under the same mutation, so the gap was real.
  Restored → passes.
- Leaks/deadlocks: none. The channel has a buffer of 1 and only one send can
  happen; a failed client read closes the client conn, which unblocks the
  server write. `waitDone` runs only after a successful read, bounded 10 s.
- Nit (accepted, no change): with the bug present, the client-side Fatalf
  fires first, so the server's i/o-timeout error is not printed. Waiting on
  the server first could stall the 1 MiB write.

**Verdict:** approve.
