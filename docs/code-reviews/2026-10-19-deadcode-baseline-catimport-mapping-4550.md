# Review: deadcode baseline — catimport mapping API (ut-docs#4550)

Date: 2026-10-19 · Lane: `lane:cloud-41`

**What.** Two entries are added to `scripts/ci/deadcode-baseline.txt`:
`catimport.MappableFields` and `catimport.ValidateMapping`, from #2059.

**Why they're not dead.** Both are the shared module's consumer API for
ut-cloud's catalogue import mapping endpoint (ut-docs#4683). They are
exported for another module, and the till has no caller. The guard only
sees the till's roots. This is the same shape as the existing
`catimport.ToRows` / `RowFor` entries.

**Exit.** Each entry goes when the till gets a caller, for example a
mapping screen on the till's own import page. Until then the entries stay.
Both are covered by `catimport/mapping_test.go`.

**Checked.** The change is baseline-only, as
`guard-deadcode-baseline-growth.sh` requires. The entries are sorted the
way the file is. No other line changed.

Verdict: safe to merge. The intent was checked against the CI output of #2059's `desktop-shell` job.
