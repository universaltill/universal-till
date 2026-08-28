# Code review — catalog import no longer silently drops rows with a reused PLU (ut-docs#1222)

- **Date:** 2026-08-28
- **Branch:** `fix/1222-catalog-import-plu-dedup`
- **Reviewer:** independent reviewer (Opus, different model from the implementer, fresh context, no prior visibility into the implementation reasoning)
- **Verdict: safe to merge**, after one blocking finding (B1) was fixed and independently re-verified.

## What shipped

The `.bkp` importer (`internal/catimport/bkp.go`) keyed catalog rows by the
source's `ProductNumber` (PLU). When the source POS reused one PLU across
several distinct products — verified against the real pilot backup, where
PLU `30006` was shared by six different drinks — only the first row
imported; every later row was flagged `IssueDuplicateSKUInFile` and
silently, permanently dropped (not even forceable via the Preview's "Import
anyway" override). This lost 13 of 229 products from the real pilot
backup: 11 from PLU collisions and 2 from whitespace-only `ProductNumber`
values that also collided with each other.

Fix:
- A row that's otherwise clean but whose PLU was already claimed by an
  earlier row in the file now gets a synthesized SKU (`PLU-2`, `PLU-3`, …)
  instead of being dropped, flagged via a new non-blocking
  `SKUIssue`/`SKUIssueRaw` pair (mirrors the existing `BarcodeIssue`/
  `TaxIssue` pattern) so the operator sees the reuse rather than a silent
  loss.
- A whitespace-only `ProductNumber` is `TrimSpace`'d before it's used as a
  SKU or a dedup key at all, so it's treated exactly like an absent one
  (empty SKU → `NULL`, ut-docs#1176) instead of colliding with another
  whitespace-only row.
- A row that ALSO carries a blocking `Issue` (missing name/bad price) is
  deliberately left out of the new dedup logic — two nameless rows sharing
  a PLU stay genuinely ambiguous, and are still resolved by
  `import_page.go`'s existing forced-correction in-file-duplicate veto
  (ut-docs#601 review F1), unchanged by this fix.
- New i18n key `import.status.sku_reused_in_file` added to all four
  locales (ar/en/fa/tr); the `catalog` help topic updated in all four to
  describe the new behaviour (no screen changed, so no screenshot
  regeneration needed).

## Findings

### B1 — BLOCKING (found, fixed, re-verified): a synthesized suffix could collide with a genuine PLU elsewhere in the file

The first version of the fix synthesized a candidate suffix (`PLU-2`,
`PLU-3`, …) checking only against SKUs already claimed by *earlier* rows
in a single forward pass. It never checked whether the candidate matched a
**distinct, genuine** `ProductNumber` belonging to a *later* row in the
same file.

Reproduced by the reviewer: a file with `ProductNumber` values `555`,
`555`, `555-2` (the third row's PLU is real and distinct, and happens to
read exactly like the naive synthesis for the second `555`). The second
row claimed `555-2` first; the third row's own `CreateItemTx` then hit
`UNIQUE constraint failed: items.sku` and was dropped with a bare
"item could not be created" — the exact "baffling `item_failed`" outcome
the ut-docs#601 F1 comment in `import_page.go` says must never happen,
just relocated from the parser to the DB write.

**Fix:** `bkp.go` now makes a first pass over every product row (before
the main parse loop) to collect the trimmed `ProductNumber` of every row
in the file — not just the ones that end up importing — into `allPLUs`.
The suffix-search loop then rejects a candidate that is either already
claimed this run (`seen`) **or** is itself a genuine product number
appearing anywhere in the file (`allPLUs`), searching forward
(`PLU-2`, `PLU-3`, …) until it finds one that's neither. This fully
disambiguates "real PLU that happens to look like a synthesized suffix"
from "synthesized suffix", independent of file order — verified with a
regression test (`TestParseBkp_SynthesizedSuffixNeverCollidesWithARealPLU`)
using the reviewer's exact `555/555/555-2` shape: all three products land,
under three distinct SKUs, and the genuine `555-2` row keeps its own real
number rather than being pushed aside by the synthesized one.

### N1 — non-blocking, accepted as scoped: a missing-name row sharing a PLU is not auto-deduped

The removed switch arm's original comment noted that a row *both* missing
a name *and* PLU-colliding must report the non-forceable duplicate issue,
so the preview never offers a doomed "Import anyway" correction. That
specific ordering is gone; such a row now reports `IssueMissingName`
(forceable), the operator can type a name, and only then does the
existing F1 veto in `import_page.go` reject it as an in-file duplicate.
Correctness is preserved — nothing lands with a broken state, and
`TestImport_BkpStagedCommitNeverForcesInFileDuplicatePLU` (the F1
regression pin) still passes unchanged — but the operator is invited to
correct a row whose correction is guaranteed to be refused.

**Accepted, not fixed in this PR.** This card's acceptance criteria only
require that a reused PLU not drop a row when the row is otherwise clean;
extending auto-dedup to a row that *also* needs a name/price correction is
a real but separate, smaller follow-up (auto-dedup only once the operator
has supplied the missing piece) — filed as a new Backlog card rather than
folded into this fix.

### N2 — non-blocking, deferred: the Preview screen doesn't show which rows will be deduped, or the new SKU

The `SKUIssue` warning is only produced in the **commit** loop, matching
how `BarcodeIssue`/`TaxIssue` already behave (not a new gap this fix
introduces) — but it means the Preview screen the operator studies before
committing shows a deduped row as plain "ok". The commit-time summary also
names only the *original* PLU, never the synthesized SKU it was assigned.
Both are real usability gaps, both pre-existing in shape (the same is true
of the barcode/tax warning pattern), and both are cheap to fix — but doing
so touches the Preview-time annotate loop and would need a second `%s` in
the locale string across all four locales, which is more diff than this
already-large fix should absorb in one pass. Deferred to a follow-up card.

### N3 — non-blocking, accepted: `IssueDuplicateSKUInFile` is now dead as a parser output

No parser sets it any more — confirmed the CSV `Parse` path never did
either. `translateImportIssue`'s case for it is unreachable; the constant
itself can't be deleted outright since
`TestForceableImportIssueAllowList` (`import_problem_grid_test.go`) still
asserts it against the forceable allow-list as a defensive check. Left in
place deliberately — removing it would touch more surface (the allow-list
test, the translate switch) for a purely cosmetic cleanup with no
behavioural stake.

### N4/N5 — fixed inline during review

- N4 (readability): the suffix-search loop reads as a plain search-for-a-
  free-slot now (see B1's fix), which resolved this alongside the
  collision fix.
- N5 (weak test assertion): `TestImport_BkpCommitDedupesReusedProductNumberInsteadOfDropping`
  asserted only `strings.Contains(resp, "30006")`, which would also pass
  if the translation key fell through to Sprintf's `%!(EXTRA …)` default-
  verb output. Strengthened to assert the actual rendered (HTML-escaped)
  status string.

## Verification performed

All commands run against the working tree on `fix/1222-catalog-import-plu-dedup`.

| Check | Result |
|---|---|
| `gofmt -l internal/catimport internal/pages` | empty |
| `go build ./...` | pass |
| `go vet ./...` | pass |
| `go test ./...` (whole module) | pass, no failures |
| `go test ./internal/catimport/... ./internal/pages/... -race` scoped to the new/changed tests | pass |
| `guard-data-access.sh` | pass — no SQL added outside `internal/data`/`internal/db` (the diff's raw `dp.Db.QueryRow` calls are in `_test.go` files, which the guard scopes out) |
| `guard-i18n.sh` | pass — 1301 keys resolve, all four locales match `en.json` (each carries the new key with exactly one `%s`, same position) |
| `guard-help-topics.sh` | pass |
| `guard-compliance-claims.sh` | pass — the new manual prose is a factual capability description, no compliance-outcome claim |

### Independent re-verification of the TDD claims

Two separate revert/restore passes, both done atomically within a single
turn (no turn boundary between revert and restore, per the reviewer
skill's stop-hook-commit guardrail):

1. **Whole fix**, before B1 was found: reverted `internal/catimport/bkp.go`,
   `catimport.go` and `internal/pages/import_page.go` to `main`'s versions
   (tests left in place). The package failed to **compile** —
   `res.Items[i].SKUIssue undefined`, `undefined: SKUIssueDuplicateInFile`
   — confirming the new tests are load-bearing on the fix's actual new
   surface, not incidentally passing. Restored and confirmed green again.
2. **B1's fix specifically**: the reviewer reproduced the collision against
   the pre-B1-fix code (`555/555/555-2` → the third row's real PLU lost to
   a `UNIQUE constraint failed: items.sku` / "item could not be created"),
   independently of and before the fix landed. After the fix,
   `TestParseBkp_SynthesizedSuffixNeverCollidesWithARealPLU` — built from
   that exact reproduction — passes; re-ran it against the pre-fix
   suffix-search logic (checking only `seen`, not `allPLUs`) and confirmed
   it fails with the collision the reviewer described, then restored.

### Recurring bug classes checked

- File-write handler missing `os.MkdirAll`: not applicable — the diff adds
  zero new file I/O. The only filesystem call in `bkp.go`
  (`os.CreateTemp("", …)`) is pre-existing and unchanged.
- A cwd-relative path where `paths.Data(...)` belongs: not applicable, no
  new path handling in the diff.

### Other checks

- No real client/shop name in any new test fixture or fixture data — the
  new tests use "Slush Matcha"/"Affogato"/"ICE cream"/"Alpha"/"Beta"/
  "Gamma" and a generic `Backup 2026-08-09.bkp` filename; several product
  names plausibly echo the real pilot catalogue (used earlier, unchanged,
  in the issue itself) but carry no shop, merchant, person or address
  identifier.
- No secret-shaped literal anywhere in the diff.

## Deferred (new Backlog cards, not silently dropped)

- N1: auto-dedup a row that needed a missing-name/bad-price correction
  first, once the operator supplies it.
- N2: surface the dedup warning (and the assigned SKU) on the Preview
  screen, not only in the post-commit summary.
