# Review: deadcode baseline for the catimport smart-import API (ut-docs#4747)

Baseline-only change, per ut-docs#3406 (`guard-deadcode-baseline-growth.sh`):
the only file changed besides this record is `scripts/ci/deadcode-baseline.txt`.

The 50 new entries are the functions of `catimport/smart*.go` (ADR-0168):
`ParseProducts`, `ParseStock`, `ParseCustomers`, `DetectSource`, `DetectKind`
and the helpers only they call. They are the shared module's public API for
ut-cloud's import job (ADR-0168 §3, cards E/F/G); the till itself does not
call them, so `deadcode` over the till's roots reports them unreachable, the
same situation as `ToRows`/`RowFor` (ut-docs#4525) and `ValidateMapping`
(ut-docs#4550). They leave the baseline when a till-side caller exists or
the module is analysed on its own.

The entries are exactly what `scripts/ci/guard-deadcode-baseline.sh` printed
for the feature branch (feat/4747-catimport-smart), merged into the file in
C-locale sorted order; no existing line was removed or edited.
