# Code review: `eod_closes` export payload (host side)

- **Card:** universaltill/ut-docs#1005 (dependency spine #1003 → #1004 →
  #1005; #1003 merged as universal-till#524, #1004 merged as
  universal-till#531)
- **Repo:** `universal-till` (companion review:
  `ut-plugin-tax-de/docs/code-reviews/2026-08-26-datev-buchungsstapel-day-close-export.md`
  for the DATEV builder that consumes this payload)
- **Dev:** independent Fable subagent (complexity:hard build tier), two
  rounds (initial build + fix round)
- **Reviewer:** independent Opus subagent, fresh context each round
  (complexity:hard review tier — deliberately not Fable, so review doesn't
  share the builder's blind spots); round 2 scoped to the round-1 fixes,
  earned by round 1's blocker-class findings

## What shipped

`exportRequestPayload` gains `EODCloses []data.EODCloseExport` (host →
plugin, `export.requested.ask`): every archived day-close's payment-method
x VAT-rate cross-tab in `[from, to]`, gated the same way as `items`/
`tax_codes` — the resolved entry must declare `"eod_closes"` in `Entities`
**and** hold `sales:read` (reused, not a new permission name).

- `internal/data/export_repo.go`: `EODCloseExport{ZNumber, Report}`,
  `POSRepo.EODClosesForExport(ctx, from, to)` (thin wrapper over
  `ArchivedReportsInRange` + `EODClosesFromArchive`), `EODClosesFromArchive`
  (filters to `kind=="eod"`, unmarshals `content_json`, skips — logs, not
  fatal — a corrupt row) — always returns a non-nil `[]EODCloseExport{}`,
  never nil, so absence vs. genuine-zero-in-range stays wire-distinguishable.
- `internal/pages/data_api.go`: wires `wantsEODCloses` (entity declared)
  and the `EODCloses` gather into the existing export-dispatch handler.
- `reference/plugin-manifest.md` (ut-docs): documents the new `entities`
  vocabulary entry.

## Independent review, round 1 — findings

Two of the five blockers found in this cycle were host-side (numbering
kept from the joint review; B1/B3/B4 are the plugin-side findings, covered
in the companion record):

- **B2 (BLOCKER) — `omitempty` on `EODCloses` destroyed the
  absence-vs-empty signal.** `EODClosesForExport` deliberately returns
  `[]`, not nil, for a supported-but-empty range specifically so the
  plugin can distinguish "declared+granted, zero closes" from "not
  declared/not granted/pre-#1005 host" — but `omitempty` collapsed both to
  an absent field on the wire, destroying the exact distinction the design
  depended on.
- **B5 (BLOCKER) — the unrelated 50,000-sale export cap 400s the closes
  export.** The `eod_closes` gather sat *after* the existing
  `CountSalesForExport`/cap/`SalesForExport` block, so an entry that
  declares `eod_closes` (and never reads `Sales` at all) still paid for,
  and could be rejected by, a cap sized for the per-sale ledger — capping
  the flagship full-year DATEV export (~365 archived closes, but
  potentially >50,000 underlying sales for a busy shop) on a row count it
  never consumes.

**Fixed:** dropped `omitempty`; moved the `eod_closes` gather to resolve
immediately after the `sales:read` permission check, and skip the
`CountSalesForExport`/cap/`SalesForExport` block entirely whenever
`wantsEODCloses` is true. Added
`TestExportDispatch_EODClosesEntrySkipsSalesGatherAndCap` (cap forced to
2, three sales seeded, request succeeds with `"sales":null`) and
`TestExportDispatch_EODClosesFieldPresentButEmptyInRange` (pins the
`"eod_closes":[]` wire shape for a supported, empty-in-range request).

## Independent review, round 2 (scoped to the round-1 fixes) — findings

No host-side blockers survived round 2. Two nits, both addressed:

- The two existing "omits" tests' fatal-message wording still said
  "omitted" where the field is now always present as `null` — assertions
  were already correct, only the strings were stale. **Fixed**: reworded
  to "expected eod_closes null …".
- `reference/plugin-manifest.md`'s `eod_closes` paragraph didn't document
  either of round 1's new contracts: the `[]`-vs-`null`
  distinguishable-emptiness rule, and that an entry declaring `eod_closes`
  gets `sales:null` unconditionally (the cap-skip rule). **Fixed**: both
  now spelled out explicitly, mirroring how `items`/`tax_codes` already
  document their own emptiness contract.

Round 2 also re-verified (not just re-read) the round-1 fixes via targeted
mutation (each reverted): flipping `!wantsEODCloses` back to always-run
makes the skip-cap test fail with the real 400; restoring `omitempty` makes
the present-but-empty test fail; confirmed via Go semantics + a passing
test that `[]byte("[]")` unmarshals non-nil and `"null"`/absent unmarshals
nil, matching the plugin's `!= nil` routing check.

## Independently re-verified this round (orchestrator, not just the Dev/Reviewer subagents' claims)

- `gofmt -l .` clean, `go build ./...` clean.
- `go test ./internal/pages/... ./internal/data/...` and full
  `go test ./...` — all green.
- `guard-data-access.sh`, `guard-kiosk-engine.sh`, `guard-i18n.sh`,
  `guard-compliance-claims.sh`, `guard-help-topics.sh`,
  `guard-docs-shots.sh` — all green.
- Read `internal/data/plugin_repo.go`'s `ReconcilePluginSettings` directly
  (not just trusted the Dev's comment) to confirm it only deletes
  undeclared keys and inserts-if-absent, never overwrites a stored value
  with the manifest default — relevant to the companion plugin-side B1 fix.

## Not changed

- The `items`/`tax_codes` gating shape this feature mirrors was not
  touched; both keep working exactly as before.
- `EODClosesFromArchive`'s corrupt-row-skip-and-log behavior (from the
  initial build) was reviewed both rounds and left as designed.
