# Review: deadcode baseline for internal/devicepay (ut-docs#3048)

Baseline-only change, per ut-docs#3406 (`guard-deadcode-baseline-growth.sh`):
the only file changed besides this record is `scripts/ci/deadcode-baseline.txt`.

The 6 new entries are the functions of `internal/devicepay/cloudcall.go`
(the Tap to Pay cloud caller, ut-docs#3048). The package is reachable only
through the gomobile package `mobile`, which is not a `deadcode` root, so the
till's roots report it unreachable, the same situation as the catimport API
(ut-docs#4747). The entries leave the baseline when the Go payment chain
(ADR-0125 section 3) calls the package from a root.

The entries are exactly what `scripts/ci/guard-deadcode-baseline.sh` printed
for the feature branch (feat/3048-android-tap-to-pay), merged in C-locale
sorted order; no existing line was removed or edited. No behaviour change.
