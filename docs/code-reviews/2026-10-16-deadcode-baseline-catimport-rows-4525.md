# Review — deadcode baseline: catimport ToRows/RowFor (ut-docs#4525)

- **Date:** 2026-10-16 · **Author:** Opus 5.5 (lane:local)
- **Change:** two baseline entries, in a PR that changes only the baseline (ut-docs#3406 rule).
- **Why they aren't dead:** `catimport.ToRows` and `RowFor` are the public API of the separate Go module `github.com/universaltill/universal-till/catimport` (#4525, ADR-0164). Their consumer is ut-cloud's import jobs (#4528), so they are unreachable from the till binary by design. The till's whole-program deadcode pass compiles the module through the root `replace`. Investigated per the guard's instructions: there are no till callers to add without inventing one.
- **Burn-down:** remove both entries if the till ever uses ToRows (for example catsync file imports, ADR-0164 card 2, #4526).
- **Verdict:** a justified false positive.
