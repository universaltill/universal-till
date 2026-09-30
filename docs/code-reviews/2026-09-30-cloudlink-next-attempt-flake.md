# 2026-09-30 — cloudlink: deflake TestNextAttemptClearsWhenTheTimerFires

Found by lane:cloud-24 when `main` CI went red after merge 7b1ea5e. The `ci`
build job failed in `internal/cloudlink`, a package that merge doesn't touch.

**Root cause (a real test bug, not runner noise).** The redial wait is `RedialSpread * rng()` with a
real random `rng`. When the draw is near 0, the client sets and clears the
next-attempt time within the 2 ms between two `eventually` polls. The test then
times out on "a next-attempt time". It reproduces every time with `rng` stubbed
to 0 (FAIL, `client_test.go:629`, the same message as the CI log).

**Fix, test-only.**
- New `newHarnessClient(t, mod, cmod)`. It stubs the Client before `Run` starts,
  so there is no data race on `rng`; `newHarness` delegates to it.
- The test pins `rng` to the top of the spread and widens `RedialSpread` from
  50 ms to 200 ms, so the next-attempt time stays visible for about 200 ms.
- Product code is unchanged.

**Verification.**
- `go test ./internal/cloudlink/ -race` passes.
- The test ran 100 times in a row and passed every time.
- With `rng` stubbed to 0 it fails, which confirms the diagnosis.

Reviewed by Fable: approved. It confirmed the diagnosis: the original test failed 2/200 runs, and with rng stubbed to 0 it fails 3/3. It also passed 100 runs under CPU load and 50 under -race. Its two nits (comment wrap, this wording) are applied.
