# Code review: catalog import surfaces BOTH missing-name and bad-price at once (ut-docs#1713)

**Branch:** `fix/1713-catalog-import-both-name-and-price-missing`
**Author:** Farshid Mirza (pipeline, lane:cloud-54)
**Reviewer:** independent Opus subagent (isolated worktree, fresh context, no prior reasoning), per `MODEL-ROUTING.md`'s medium-complexity review tier

## What

`internal/catimport/bkp.go`'s `ParseBkp` and `catimport.go`'s `Parse` each
set at most ONE `Issue` reason code per row, via a switch that stopped at
the first defect it found. A row that was BOTH nameless AND carried an
unparseable price only ever reported `missing_name` — the price branch
never ran — so `internal/pages/import_page.go`'s problem-grid UI (driven by
`forceableImportIssue`, `internal/pages/import_stage.go`) only ever offered
a name-correction field. Once the operator supplied the name, the row's
`Issue` cleared and it imported silently at `PriceMinor` 0: no price field
shown, no warning at all. Found during independent review of ut-docs#1234.

Fix:

- New reason code `catimport.IssueMissingNameAndBadPrice`
  (`missing_name_and_bad_price`).
- Both `Parse` (CSV) and `ParseBkp` (`.bkp`) now parse the price cell
  **unconditionally**, before deciding `Issue`, so both defects can be
  detected together. This also fixed a related latent bug specific to
  `ParseBkp`: previously a row missing ONLY its name (with an
  otherwise-valid price) never had its price parsed at all — the parse
  lived only in the switch's `default` case — so `PriceMinor` silently
  stayed 0 even for a genuinely good price. `Parse` (CSV) already parsed
  price unconditionally and didn't have this half of the bug; `ParseBkp`
  now matches it.
- `forceableImportIssue` (`import_stage.go`) changed from
  `(field string, ok bool)` to `(fields []string, ok bool)` — the combined
  issue returns `["name", "price"]`.
- The commit-time override loop in `import_page.go` now requires **every**
  listed field to validate before **any** of them is applied: if `name`
  succeeds but `price` is blank/invalid (or vice versa), the row stays
  skipped with the specific missing-field note — never a partial apply.
- `rowView.FixField string` → `FixFields []string`; the preview renders one
  correction `<input>` per listed field (`row-fix-name-%d` /
  `row-fix-price-%d`, replacing the old shared `row-fix-%d`), and the
  include checkbox's `data-fix-target` now carries a space-separated list
  of target ids. `web/ui/pages/import.html`'s "required-if-ticked"
  convenience script updated to split on whitespace.
- `translateImportIssue` gained a case for the new code; new locale key
  `import.status.missing_name_and_bad_price` (`%s` placeholder for the raw
  price) added to all four locales (`en`/`ar`/`fa`/`tr`), matching the
  existing `missing_name`/`bad_price` siblings' style.
- `web/help/{en,ar,fa,tr}/catalog.md`'s "Fixing problem rows before import"
  paragraph updated to describe a row needing both corrections; screenshots
  regenerated (`make docs-shots`).

## Independent review

Full record (Opus subagent, isolated `git worktree`, revert-then-restore
TDD verification, actually ran build/vet/lint/tests/guards rather than
reading the diff alone):

- **[blocking, fixed] `web/help/img/manifest.json` was stale** relative to
  the manual edits — `guard-docs-shots.sh` failed (`build` job would have
  gone red). The manual text was committed in the same amend that
  introduced the review's own catch, but `make docs-shots` hadn't been
  re-run against it yet at review time. Fixed: re-ran `make docs-shots`;
  guard now passes (`web/help/img/manifest.json`, plus 3 more PNGs on
  unrelated pages — see "pixel-noise" note below).
- **[blocking, fixed] Fail-open defect in the new field-validation loop**
  (`import_page.go`, the override-application loop): before this card,
  `Issue` was cleared **inside** each `case`, so an unrecognised field name
  left the row correctly skipped (fail-safe). The rewrite moved the
  `Issue = ""` clearing to **after** the loop, keyed only on `rowFailed`
  — so a future third correction type with no `case` in the switch would
  leave `rowFailed == false` and silently clear `Issue`, importing the row
  with that correction never actually applied. Not reachable today (only
  `forceableImportIssue` feeds `fields`, and it only ever returns
  `name`/`price`), but it's exactly the failure class this card exists to
  close, structurally re-armed for the next person who extends the
  allow-list. Fixed: added a `default:` case that logs and sets
  `rowFailed = true`, mirroring `translateImportIssue`'s existing
  defensive-default convention for an unrecognised code.
- **[non-blocking, fixed] Legacy single-field id↔`data-fix-target` pairing
  was untested** — the existing preview assertions only checked
  `name="row_name_0"`/`name="row_price_1"`, not the renamed
  `id="row-fix-name-%d"`/`id="row-fix-price-%d"` or that
  `data-fix-target` matches. Added two assertions to
  `TestImport_PreviewStagesFileAndRendersProblemControls` covering both
  legacy (single-field) issue types, not just the new combined one.
- **[non-blocking, fixed] No end-to-end (commit) test for the second latent
  bug** — the parser-level fix (`TestParseBkp_MissingNameOnlyStillCarriesValidPrice`)
  wasn't mirrored by a commit-level assertion that a name-only correction
  on a `.bkp` row actually lands with its real price, not 0. Added
  `TestImport_BkpNameOnlyCorrectionKeepsRealPrice`.
- **[non-blocking, accepted, not fixed]** Only the FIRST failing field is
  reported when both are blank — the operator sees "no corrected name was
  given" and, after fixing that and resubmitting, only then learns the
  price was needed too, costing an extra staged-upload round trip (a
  commit consumes the staged copy regardless of outcome). Client-side
  `required` wiring normally prevents ever reaching this server-side path
  with both blank. Deferred as a UX polish item, not a correctness bug —
  noted for a future Backlog card rather than expanding this one.
- **One unreproducible test flake** noted during review (a single
  `internal/pages` run reported a bare `FAIL` with no `--- FAIL` line,
  under concurrent `go build`/`go vet` load; four subsequent runs — full
  suite, one under deliberate CPU load — all passed). Not reproduced again
  in this session's own runs either; treated as a pre-existing,
  load-sensitive flake unrelated to this diff (this change adds no new
  concurrency), not something to chase down here.
- Verified correct, no changes needed: the in-file-duplicate/reused-PLU
  dedup logic (`anchorSKU`/`inFileSKU`/`dedupeReusedPLU`) still behaves
  identically — the new combined issue is a non-empty `Issue` exactly like
  the two pre-existing ones, so it never seeds `inFileSKU`/`anchorSKU` at
  parse time and never registers its PLU in `bkp.go`'s `seen` map;
  `IssueDetail` is populated consistently across the CSV and `.bkp` paths;
  i18n keys/placeholders/escaping are correct end-to-end (rendered status
  verified as `missing name and bad price: not-a-price`, HTML-escaped); no
  SQL outside `internal/data`/`internal/db`; no new file I/O; no real
  client/shop names in fixtures.
- **Pixel-noise PNG churn confirmed benign**: one changed screenshot
  (`ar/till-designer.png`) diffed at 32 of 1,843,800 decompressed pixel
  bytes (0.0017%), same dimensions — font-antialiasing noise from a
  mismatched local Chromium build vs. the pinned version
  (`guard-docs-shots.sh`'s own warning), not a regression. Both it and
  `en/sell.png` render as normal, correct screens.

**Verdict: not safe to merge as first submitted** (2 blocking findings) —
**both fixed in this same commit; safe to merge now.**

## Verification

- `gofmt -l` clean on every changed `.go` file; `go build ./...` clean;
  `go vet ./...` clean.
- `go test ./...` — full suite green (49 packages).
- `golangci-lint run ./...` — 0 issues.
- All 18 CI-blocking `build`-job guards run locally and pass:
  `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-page-http-error.sh`,
  `guard-i18n.sh`, `guard-compliance-claims.sh`, `guard-docs-shots.sh`,
  `guard-help-topics.sh`, `guard-webkit-version.sh`,
  `guard-kiosk-launch-flags.sh`, `guard-android-status-address.sh`,
  `guard-android-i18n.sh`, `guard-emoji-font.sh`, `guard-htmx-loaded.sh`,
  `guard-autofill-suppression.sh`, `guard-e2e-fixtures-import.sh`,
  `check-brand-assets.sh`, `guard-makefile-version.sh`.
- **TDD re-verified personally by the reviewer** (isolated worktree, not
  the orchestrator's shared checkout): reverted `bkp.go`'s unconditional
  price parse back to the old `default:`-case form —
  `TestParseBkp_MissingNameAndBadPrice` and
  `TestParseBkp_MissingNameOnlyStillCarriesValidPrice` both failed with
  exactly the described wrong behavior (`Issue` stuck at `missing_name`,
  `IssueDetail` empty; `PriceMinor` 0 instead of the real price). Reverted
  `import_page.go`'s validation loop to the pre-fix single-field `switch` —
  the `name_only`/`both_together` subtests of
  `TestImport_CommitStagedBothNameAndPriceRequireBothCorrections` failed
  with exactly the described silent-£0.00 regression. Both reverts
  restored; full suite green again.
- Locale key set verified identical across `en`/`ar`/`fa`/`tr`
  (`web/locales/*.json`), each with exactly one `%s` placeholder.
- Manual (`web/help/{en,ar,fa,tr}/catalog.md`) updated in this branch to
  describe a row needing both corrections; screenshots regenerated via
  `make docs-shots` (guard green).

Closes universaltill/ut-docs#1713
