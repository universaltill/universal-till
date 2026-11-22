# Review: tax auto-install first-load check, test gaps + rate_bp bound fix (ut-docs#6286)

Date: 2026-11-22. Lane `lane:cloud-41`. ADR-0172 §3 ("first-load check, before activation"), slice 2 step B.

## What shipped

The first-load dry `tax.rate.ask` itself shipped in `7884ef6a` (ut-docs#4837): `DryAskWasm`
(`internal/plugins/dry_ask.go`) and `taxFirstLoadCheck` / `validTaxAskAnswer`
(`internal/pages/tax_autoinstall.go`). It runs as the automatic tax install's `PreInstall` gate,
after signature verification and before revocation, the move to the live directory and
`PersistManifest`. `TestTaxFirstLoadCheck` already covered trap, malformed and decline. This
change closes the gaps that were left:

- `TestValidTaxAskAnswer`: a boundary table (empty/whitespace, non-object, no `rate_bp`, 0, 10000,
  -1, 10001, float, string, `null`, `{"rate_bp":null}`, and the two case-variant rows below).
- `TestTaxFirstLoadCheck_RefusesNonWasmAndModuleless`: a non-wasm or empty runtime, no module, and entrypoint `./`.
  All are refused before the sandbox runs.
- `TestTaxFirstLoadCheck_HangingGuestIsRefusedWithinDeadline` (new guest
  `internal/pages/testdata/taxhang_guest`): refused with `context.DeadlineExceeded` in 2–12 s.
  Nothing active, nothing on disk, spec still pending.
- `TestTaxAutoInstall_UnverifiedBundleRefusedBeforeDryAsk`: the `taxbadrate` guest signed with a rogue key.
  The refusal is the verifier's, never a first-load error. Nothing is persisted.
- **Production fix.** `validTaxAskAnswer` looked up the exact map key `rate_bp`, but core decodes
  into `taxRateAskResponse`, and `encoding/json` matches keys case-insensitively, last one winning. So
  `{"RATE_BP":99999}` and `{"rate_bp":5,"RATE_BP":99999}` passed the 0..10000 check, and core then
  read 99999. The check now decodes into the same struct core uses.

## Review

- Dev: Sonnet. Independent review: Opus 5.5, fresh context. Tester: Opus 5.5.
- Verdict: approve after fixes. All fixes are in.
  - **F4 (important, fixed).** The case-insensitive key bypass above. TDD: the two rows were red (`err=<nil>`), then green after the fix.
  - **F1 (should-fix, fixed).** The hang bound was 8 s. A loaded 1-core run measured 8.04 s, so it is now 12 s. That still catches a 2 s→12 s per-ask mutation (~14 s).
  - **F2 (should-fix, fixed).** The hang test accepted any refusal. It now also asserts `elapsed >= 2s` and `errors.Is(err, context.DeadlineExceeded)`.
  - **F3 (should-fix, fixed).** R4 signed a guest that passes the dry ask, so a reversed order (ask, then verify)
    stayed green. It now signs `taxbadrate`, which fails the dry ask.
  - Nits, not fixed: no check that the busy-loop guest's goroutine is gone after close (wazero `WithCloseOnContextDone`).
    The 15 s compile cap `dryAskTimeout` is untested; the 2 s per-ask deadline is what bounds a hang.
  - Checked and not a defect: `null` and `{"rate_bp":null}` pass, and core reads 0 for them ("no opinion").
- Follow-up noted on the card: the runtime reader (`tax_hook.go`) has no upper cap on `rate_bp` either.

## Verification

- Mutants, each one mutated, run and restored:
  - Dev: `>`→`>=`, `<`→`<=`, empty early-return off, runtime check off, no-module check off. Its text assertion was tightened after the first mutant survived.
  - Dev: per-ask timeout 2 s→12 s and VerifyManifest error ignored. All mutants above red.
  - Tester: independently re-ran the timeout and VerifyManifest mutants; both red.
- Gates: `gofmt -l internal/pages` is empty and `go vet ./internal/pages` passes. `go test -count=1 ./internal/pages -run 'TaxFirstLoad|TaxAutoInstall|ValidTaxAsk|TaxHook|TaxRate'`
  passes (orchestrator re-run, 45.6 s; Tester ran it twice). These guards pass: guard-fixed-dates, guard-data-access, guard-i18n,
  guard-core-neutral, guard-mail-addresses, guard-kiosk-engine, guard-no-native-confirm. The full suite runs in CI.

Pre-flight: help topic n/a (no user-visible change); en.json n/a; README n/a; reference/ n/a (ADR-0172 unchanged, the fix enforces its §3 as written); review record updated (this file); follow-ups: runtime rate_bp cap noted on ut-docs#6286.
