# Review — PR #2326 merge of main + P8 flake fix (ut-docs#5330)

Scope: merge commit of `main` into `feat/5330-pending-link-joiner` (conflict:
`web/help/img/manifest.json` only, regenerated with
`e2e/tests-docs/write-manifest.js`, checked by `guard-docs-shots.sh`; the PNGs
cannot be re-shot off Linux because of the font pin, the guard checks hashes
only) plus a test-only change in `internal/pages`.

## Root cause of the `pages-shuffle` red

`pendingLinkRunner.start` runs `watchPending` on its own goroutine, so
`pairStartVia` returns before the fake watch has run.
`TestPendingJoin_P8_SecondPairStartStopsTheFirstWatch` read the shared
`stopped` slice straight after the second `pairStartVia`: the second watch
may not have appended yet ("1 watches started, want 2"). 
Fix: the fake signals a buffered `started` channel after appending; the test
waits for both signals with `waitCh` (event, no sleep, no skip, nothing
weakened).

## Semantic merge breaks (textually clean, did not compile)

- `syncBuf` clashed with main's `locale_region_5751_test.go`: ours renamed
  `pendingLogBuf`.
- `sync_reader_guard_test.go`, `till_name_join_length_5898_test.go` called the
  old `registerPairingJoinAPI(mux, d)`: now `registerPairingJoinAPIT(t, ...)`.

## Tester (fresh agent)

PASS. vet/gofmt clean; P8 x200 green; race run of all 17 `TestPendingJoin*`
green. Proved the test: with a 1.5 s delay injected in the second fake watch
the old logic fails with the reported message, the new one passes.

## Reviewer (fresh agent)

- Medium, **accepted and fixed**: start order. `rp.links.start` (pairing_join.go:518)
  runs before `rp.set` stops the old watch, so the two appends were unordered
  (low probability: a real HTTP round trip sits between). The test now waits
  for the first start before the second `pairStartVia`. (My first rebuttal,
  that `stop()` orders them, was wrong.)
- Info: first watch is always called even if cancelled early; no deadlock
  (mu released before the send; a third start would block on the cap-2
  channel, this test makes two); no other test has the pattern; merge lost no
  intent from either side in `pairing_join.go` / `multitill.md`.

Pre-flight: PREFLIGHT.md walked for a test-only change: no new keys, UI, paths or secrets.

Known residual: one run of `-shuffle=7` failed once while a tester ran heavy loops in parallel (output truncated, test not identified); a clean rerun of seed 7 passed.
