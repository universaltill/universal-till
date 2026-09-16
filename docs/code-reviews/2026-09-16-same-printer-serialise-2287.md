# Review — receipt + kitchen ticket on the SAME printer: serialise and retry per destination (ut-docs#2287)

**Date:** 2026-09-16 · **Lane:** local · **Card:** ut-docs#2287 (p1, complexity:medium, 2026-09-16 design batch)
**Build:** Sonnet subagent, TDD · **Review:** fresh-context Opus subagent, read-only · **Fix triage:** local lane

## Bug
Receipt printer and a kitchen station pointed at the same network printer → after a sale only one document came out. `printReceiptAsync` and `printKitchenAsync` both fire on `d.AsyncWork`; each opened its own TCP connection; single-connection ESC/POS printers refuse/reset the second dial and the job was lost with only an audit row.

## Fix (`internal/print/transport.go`)
- Per-destination ctx-aware lock (`destLocks` sync.Map of 1-buffered chans, key `network:<host:port>` / `device:<path>`) held for the whole dial+write / open+write. Different printers stay concurrent; no global lock.
- Bounded dial retry: 3 attempts, 300 ms / 600 ms, only on ECONNREFUSED / ECONNRESET (io.EOF accepted for injected test dialers only); never on DNS not-found or ctx cancellation; **write errors are never retried** (partial-print / double-print risk).
- Device path holds the lock from inside the write goroutine until `write(2)` returns (review finding 2).

## Tests (`transport_test.go`, `transport_unix_test.go` — new)
Same-address serialisation (client-side timestamps bracket the guarded span; failed 4/5 pre-fix), different-address concurrency (hold inside `dialFn`; **verified by mutation**: a global lock key makes it fail at 603 ms vs 600 ms bound), retry-on-refused with fake dialer, write-error-not-retried via `net.Pipe`, FIFO interleave test for the device transport (`!windows`).

## Review findings and outcome
| # | severity | finding | outcome |
|---|---|---|---|
| 1 | should-fix | different-address test was vacuous (hold on server read; passed with a global lock) | fixed: hold moved into `dialFn`; mutation-checked |
| 2 | should-fix | device transport released the lock while `write(2)` could still be blocked (out of paper) → later interleave | fixed: `release()`/`f.Close()` deferred inside the write goroutine |
| 3 | nit | io.EOF cannot come from a TCP dial | comment corrected |
| 4 | nit | conn leaked if a fake dialer returns conn+err | closed |
| 5 | nit | `syscall.Mkfifo` breaks `GOOS=windows go vet` on the test package | FIFO test moved to `transport_unix_test.go` (`//go:build !windows`); vet passes on darwin + windows |
| 6 | nit | lock-wait timeout indistinguishable from dial timeout in the audit row | `printer busy <addr>` vs `printer connect <addr>` |

Clean: lock leak paths, address-key normalisation (`:9100` applied once in `NewTransport`), no SQL, no user-facing strings, gofmt/vet/golangci-lint.

## Device test (thermal printer 192.168.1.111, real hardware)
Scratch till, `printer.address` = `printer.kitchen_addr` = `192.168.1.111:9100`, one cash sale per build. Both the unpatched `main` build and the fixed build returned success on both jobs (no `receipt_print_failed_at` / `kitchen_print_failed_at`, no failure audit row) — this particular printer *accepts* a second connection, so the loss the product owner saw on the pilot's hardware could not be reproduced from software signals alone; the paper output is the product owner's to confirm (asked in the session). The serialisation/retry is proven by the unit tests against a single-connection fake and is strictly safer on a printer that accepts both.
