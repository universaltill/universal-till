# Review: deadcode baseline for internal/paybroker (ut-docs#3049)

Baseline-only change, per ut-docs#3406 (`guard-deadcode-baseline-growth.sh`):
the only file changed besides this record is `scripts/ci/deadcode-baseline.txt`.

The 4 new entries are `internal/paybroker` functions added by the Tap to Pay
on iPhone feature branch (`feat/3049-ios-tap-to-pay`): `Call`, `Allowed`,
`HTTPError.Error` and `errorCode`. The till's main program imports the package
only to install the cloud target resolver; the one caller of `Call` today is
the gomobile export `mobile.TapToPayCloud`, and `mobile/` is not a deadcode
root — the same situation as `kiosk.SetBridge`. They leave the baseline when
the WASM host function `payment_provider_call` (ut-docs#6455) calls `Call`
from the main program.

The entries are exactly what `scripts/ci/guard-deadcode-baseline.sh` printed
for the feature branch, merged into the file in C-locale sorted order; no
existing line was removed or edited.

Pre-flight: baseline-only; reviewed by the orchestrator against the guard
output; no code change.
