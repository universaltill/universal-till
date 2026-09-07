# Catalog import: dedupe a forced-corrected row anchored by a clean PLU twin (ut-docs#1234)

## What shipped

ut-docs#1222 gave a clean `.bkp` row that reuses another row's PLU a
synthesized suffix (`PLU-2`, `PLU-3`, …) instead of silently dropping it
— but deliberately left alone a row that ALSO carried a blocking issue
(missing name / bad price): that row still fell through to the
forced-correction in-file-duplicate veto (ut-docs#601 review F1), which
refused ANY forced correction whose SKU collided with another row in the
same parse, full stop. Once an operator supplies the missing name/price,
that row is just as real and distinct a product as any clean row sharing
the same PLU — there was no principled reason it couldn't be deduped the
same way.

`internal/pages/import_page.go`'s staged-commit override-application
block now distinguishes two cases when a forceable row's SKU collides
with an already-claimed one:

- **A genuinely clean row (`Issue == ""` at parse time) anchors the PLU.**
  That row is real proof the PLU names a real, distinct product, so the
  corrected row gets its own synthesized suffix instead of an
  unconditional veto — mirroring `bkp.go`'s own suffix-dedup for two
  clean rows sharing a PLU. Every row anchored this way gets its own
  suffix, however many there are (two `dupSuffix`-tracked corrected rows
  sharing one anchored PLU each land distinctly, not just the first).
- **No clean row anywhere claims the PLU.** Genuinely ambiguous —
  unchanged from before this card (ut-docs#601 review F1's own case):
  only the first forced correction may land, every later one stays
  vetoed.

`anchorSKU` (seeded once, from clean rows only) tells the two cases
apart; `inFileSKU` (every claimed SKU, clean or corrected) and
`allRawSKUs` (a pre-mutation snapshot of every row's original SKU) keep a
synthesized suffix from ever colliding with another claim or a real,
not-yet-processed PLU later in the file, the same guarantee `bkp.go`'s
own `seen`/`allPLUs` maps give the clean-row case.

Also: `web/help/en/catalog.md` extends the existing reused-item-number
note to cover a row fixed in the problem grid, and
`web/help/img/manifest.json` was regenerated (`make docs-shots`) since
the topic markdown changed.

No SQL, no new i18n keys (reuses the pre-existing
`import.status.duplicate_sku_in_file` / `sku_reused_in_file` keys), no
ADR needed — this is a bugfix extending an existing, already-reviewed
convention, not a new cross-cutting mechanism.

## Independent review

Opus, fresh context (background subagent, this session's own checkout —
read-only review, no files modified). Verification run: `gofmt -l .`,
`go build ./...`, `golangci-lint run ./internal/pages/...` (0 issues),
`go test ./internal/pages -run TestImport_`, and the
data-access/i18n/compliance-claims/docs-shots/help-topics guards — all
clean. Traced the algorithm by hand against all 4 acceptance criteria
(anchored-by-earlier-clean-row, anchored-by-later-clean-row, two
corrected rows sharing one anchor, no-anchor-stays-ambiguous) and could
not construct a case producing two rows on the same final SKU — including
the adversarial `40001` / `40001` / `"40001-2"`-as-a-real-PLU ordering
case, both directions.

**Verdict: LGTM-with-nits.** No blockers.

### Findings — the two should-fixes, both fixed

1. **should-fix — the dedupe silently extended onto the CSV import path,
   contradicting the shipped help text.** The synthesis was gated only on
   `anchorSKU[sku]`, with no format check. But reused-PLU suffix-dedup is
   an explicitly `.bkp`-only convention (`catimport.go`'s
   `SKUIssueDuplicateInFile` doc comment says so directly) — CSV's
   `Parse` has **no** in-file SKU-collision detection for clean rows at
   all. Verified empirically: a CSV row corrected via the grid, sharing
   its SKU with an unrelated clean CSV row, landed under a synthesized
   `ABC-2` — strictly *better* treatment than two clean CSV rows sharing
   the identical defect (which still just lose the second one, untouched
   by this card). **Fixed:** added `dedupeReusedPLU := res.Format ==
   "speedy-kasse"`, gating both the veto-vs-dedupe decision and the
   synthesis block; CSV now keeps this card's veto-only behavior
   unchanged. Added a throwaway probe test confirming the fix (a CSV
   correction against a clean-SKU twin now stays vetoed with the
   `duplicate_sku_in_file` status, same as before this card) — not
   committed as a permanent test since it's a negative/scope-boundary
   check the reviewer already exercised, but re-run again after the fix
   landed to confirm.
2. **should-fix — the help-doc wording misdescribed the case that stays
   stuck.** The added sentence said "the only case that stays stuck is
   two *uncorrected* rows…" — but the rows in that case ARE corrected
   (both ticked, both given names) and one still doesn't land; the real
   rule is "no already-importable row shares that number," not "missing
   the same information" (a missing-name row and a bad-price row sharing
   a PLU is the identical case). **Fixed:** reworded to "…the only rows
   that still can't be rescued are two or more problem rows sharing a
   number that no already-importable row uses — the first one you
   correct takes the number and the rest stay skipped, because there's
   nothing to say which of them the number really belongs to."
   Regenerated the doc screenshot manifest for the changed topic.

### Nits — landed anyway (cheap, no scope risk)

3. **comment inaccuracy** — a comment claimed `dupSuffix` "picks up where
   bkp.go's own per-PLU numbering left off," but the map started empty
   and only avoided collisions via the `inFileSKU`/`allRawSKUs`
   membership checks (correct outcome, wasted re-tests for a PLU shared
   by many rows). Fixed for real rather than just correcting the prose:
   `dupSuffix` is now seeded from any `"PLU-N"` suffix a clean row's own
   parse-time dedup already produced for that PLU, so it resumes
   numbering instead of re-walking already-claimed suffixes.
4. **test coverage gap — AC2 (two corrected rows sharing one anchored
   PLU) wasn't actually exercised.** Both anchored fixtures in the
   original test had exactly one corrected row. Added a third PLU
   (`70001`: one clean anchor + two missing-name rows, both corrected) to
   `TestImport_BkpStagedCommitDedupesAnchoredForcedRowsVetoesUnanchoredOnes`,
   asserting both land under distinct suffixes (`70001-2`, `70001-3`).
   Also added an assertion that a deduped row's response carries the
   operator-facing "reused in this file" status, not a silent plain "OK"
   that hides the PLU having changed.

### Deferred as a follow-up card, not fixed here

5. **pre-existing gap, widened by this card, not a regression from it** —
   a row that is BOTH nameless AND has an unparseable price only ever
   reports `missing_name` (the switch in `bkp.go`/`catimport.go` picks
   one `Issue` per row); the problem grid then offers only a name field,
   and a name-only correction silently ships the row at `£0.00`.
   Pre-existing since ut-docs#601, not introduced here — flagged because
   this card makes the *reused-PLU* variant of that row newly reachable
   (old-till `.bkp` migrations are exactly where a garbled price cell and
   a reused PLU are both plausible on the same row). Filed as
   ut-docs#1713 rather than folded into this PR — it's a genuinely
   separate design question (detect+surface both defects, and whether/how
   to re-validate price after a name-only correction), not a one-line
   fix, and this card's own scope is the veto/dedupe decision, not the
   grid's field coverage.
6. **observation, no action** — "anchor" is decided from parse-time
   `Issue == ""` only, so a clean row that itself gets skipped at
   commit-time (its SKU/barcode already exists in the catalog) still
   counts as an anchor: the corrected row takes the `-2` suffix while the
   bare PLU goes unclaimed by anyone. Harmless (no duplicate SKU, no data
   loss), just slightly suboptimal numbering — not worth a follow-up card
   on its own.

## Verified beyond automated tests

- Confirmed by hand, before writing the fix, that the extended regression
  test (`TestImport_BkpStagedCommitDedupesAnchoredForcedRowsVetoesUnanchoredOnes`)
  genuinely fails against the pre-fix code (`git stash` the fix, re-run:
  fails with `SKU 40001-2: 0 items landed, want exactly 1`; `git stash
  pop`, re-run: passes) — a real regression test, not a tautology.
- Ran the CSV-scope probe test (finding 1) both before the fix (fails —
  `ABC-2` lands) and after (passes — stays vetoed, `duplicate item number
  in this file` status), confirming the format gate is what's doing the
  work.
- Full `go test ./...` (every package, not just `internal/pages`) — green
  both before and after the review-round fixes.
- Every guard the `build` job runs was executed directly, not assumed:
  `guard-data-access`, `guard-i18n`, `guard-compliance-claims`,
  `guard-docs-shots`, `guard-help-topics`, `guard-page-http-error`,
  `guard-kiosk-engine`, `guard-plugin-menu-read`, `guard-webkit-version`,
  `guard-kiosk-launch-flags`, `guard-android-status-address`,
  `guard-android-i18n`, `guard-android-external-links`,
  `guard-emoji-font`, `guard-htmx-loaded`, `guard-autofill-suppression`,
  `guard-osk-loaded`, `guard-e2e-fixtures-import`, `check-brand-assets`,
  `guard-makefile-version` — all pass (most are no-ops for this diff's
  surface, but run rather than assumed).
- `gofmt -l .` clean, `go vet ./...` clean, `golangci-lint run
  ./internal/pages/...` 0 issues.
- `make docs-shots` run for real (this environment's pre-installed
  Chromium) after each help-doc wording change; confirmed the *only*
  image-affecting diff was the `catalog` topic's own hash in
  `manifest.json` — an incidental single-byte diff in an unrelated
  screenshot (`sell.png`, likely a live on-screen clock) from the first
  full-suite run was identified and reverted before committing, to keep
  the diff scoped to what this card actually changed.
- No visible UI surface changed by the Go fix itself (pure server-side
  logic, no new template markup) — the only user-facing surface is the
  help-doc prose edit, not a rendered page, so no screenshot/visual-check
  attestation applies beyond the regenerated doc-shots manifest above.

## Safe-to-merge

Yes, after the two should-fixes above (both landed in this same PR,
re-verified with a fresh full test run, guard set, and lint pass).

## Explicitly deferred (by design, not oversight)

- ut-docs#1713 (finding 5) — a real but independent, pre-existing gap;
  not this card's scope.
- Finding 6 — harmless numbering suboptimality, not worth a dedicated
  follow-up.
