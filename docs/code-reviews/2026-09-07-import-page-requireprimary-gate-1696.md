# Bulk catalog import: primary-only gate (ut-docs#1696)

## What shipped

`internal/pages/import_page.go`'s `POST /api/import` bulk-creates
items/categories/tax codes and attaches barcodes — writing to
`items`/`item_barcodes`, both synced shop-wide via
`sync_admin_repo.go`'s `adminTables` — with no `requirePrimary` gate at
all. This is the same bug class ut-docs#1590/#1667/#1689 already fixed
for other pages, but with a larger blast radius: a manager running a
CSV/`.bkp` import on a satellite till would have had hundreds of rows
silently reverted on the next admin pull, instead of one row from a
single-item edit.

`internal/pages/import_page.go`: a `requirePrimary`-shaped check —
`commit && d.SyncPrimaryURL(r.Context()) != ""` — added right after
`commit`/`usedFirstBootExemption` are resolved and before any multipart
body/staged-upload handling, so a refused commit never consumes a staged
preview. Preview (`commit=0`) writes nothing and stays unaffected on a
replica. The pre-admin setup wizard's `commitStagedImportForSetup`
in-process replay is unaffected — a fresh/unpaired till never has
`sync.primary_url` set (`SyncPrimaryURL` returns `""` when unset), and its
own `usedFirstBootExemption && commit` check already forbids a first-boot
commit from reaching this gate in the first place.

i18n key `import.error.replica_use_primary` added to all four locales,
phrased to match the sentence template every sibling
`*.error.replica_use_primary` key already uses (registers/locations/
tables/kitchen-stations/fiscal-register/catalog).

New regression test `TestImport_CommitRefusedOnReplica` (mirrors
`TestRegistersPage_MutationsRefusedOnReplica`'s shape): commit on a
replica refuses with 409 and writes nothing; preview on the same replica
still succeeds.

**Follow-up owed in the same cycle (not yet pushed as of this record):**
the two external language packs, `ut-plugin-language-de`/`-es`, need the
same key translated once this PR merges to `main` — `lang-pack-drift` is
blocking on push to `main` but only advisory on this PR (it never touched
`en.json` before this change existed on `main`). Translations are already
drafted and committed locally on feature branches in both pack repos
(`i18n/1696-import-replica-use-primary`), held back because
`check-key-drift.sh` in each pack correctly reports the key as an orphan
until core's `en.json` actually carries it — landing them is this cycle's
very next action after this PR merges.

## Independent review

Opus, isolated worktree (`isolation: "worktree"`, so the revert-then-
restore TDD verification below never touched this session's own
checkout).

**Verdict: not safe to merge as-is — 2 blocking findings. Both fixed,
full gate re-run clean.**

1. **The 409 was completely invisible to the operator (high, blocking).**
   `common.LocalizedError` answers `text/plain`; this page's htmx
   `force-swap` (`app.js`'s `htmx:beforeSwap`, ut-docs#916) only picks up
   a real `text/html` fragment, and `/import` has no `#pos-alert`
   equivalent for `htmx:responseError`/`showAlert` to fall back into
   (that element only exists on the sale screen). Net effect: press
   Import on a replica, the busy indicator stops, `#import-result` stays
   empty, nothing else happens — trading "silently reverted an hour
   later" for "silently does nothing now." **Fix:** render a real HTML
   notice fragment instead (`w.Header().Set("Content-Type", "text/html;
   charset=utf-8")` + `w.WriteHeader(http.StatusConflict)` +
   `httpx.RenderNotice(w, locale, "error", "import.error.replica_use_primary")`),
   the same shape `POST /api/catalog/export-save` already uses a few
   lines up in this same file. `locale`/`T` resolution hoisted a few
   lines earlier so the gate can use it. Test extended to assert
   `Content-Type: text/html` and a rendered `class="pos-notice error"`
   fragment, not just the status code and a `"primary"` substring.

2. **The user manual was not updated (medium-high, blocking under the
   repo's own standing rule).** `web/help/en/catalog.md` documents the
   Import flow in detail (including the sibling ut-docs#970
   currency-confirm gate) but said nothing about the new replica
   refusal — direct precedent in two recent sibling reviews
   (`2026-09-06-catalog-modifier-sync-primary-gate-1667.md`,
   `2026-09-05-registers-locations-primary-gate.md`) and CLAUDE.md's
   standing product-owner instruction (ut-docs#324, "the manual is only
   worth having if it is never behind the product"). **Fix:** one
   `## Good to know` bullet added to `catalog.md` in all four locales,
   matching the existing modifier-options bullet's style; screenshots
   regenerated via `make docs-shots` (100/100 Playwright specs green;
   3 unrelated PNGs — `ar/sell.png`, `ar/till-designer.png`,
   `fa/till-designer.png` — differ by a few bytes of encoder noise, same
   as the precedent review's own note; `catalog.png` itself unchanged
   since no screen's visible layout changed).

**TDD claim independently re-verified** (not just trusted): the reviewer
removed only the Go gate (test untouched) and re-ran
`TestImport_CommitRefusedOnReplica` — it failed with a real assertion
(`code 200, want 409`, body showing 2 items actually created), proving
the bug the fix closes. Restored, green again. The reviewer additionally
mutation-tested the preview half of the test (dropping the `commit &&`
guard) and confirmed it then correctly fails too — both assertions in the
new test are load-bearing.

**Also verified, not just read:** gate placement (before
`takeStagedCatalogUpload`, so a refused commit never consumes a staged
copy); the setup-wizard path traced end-to-end (`SyncPrimaryURL`'s
empty-when-unset, the only production writer of `sync.primary_url`, and
that the wizard's join branch is terminal so it can never reach
`commitStagedImportForSetup` in the same run); a promoted till regains
import via `ClearReplicaIdentity`; blast radius matches `adminTables`
exactly (categories/brands/tax_codes included, stock movements correctly
left unblocked since they aren't synced); no `os.MkdirAll`/cwd-relative-
path gap introduced; no real client/shop name or secret-shaped literal.

**Non-blocking notes, not acted on this PR:**
- The gate necessarily runs after `ParseMultipartForm(20 << 20)`, so a
  replica spools the whole upload before being refused — matches the
  existing first-boot-exemption check's own documented reasoning for why
  it can't move earlier (an early `r.FormValue` would silently widen the
  32MB default parse cap for every caller). Accepted as-is.
- `POST /api/data/import` (`import_dispatch.go`) — plugin-driven catalog
  import — has no equivalent gate and writes through a different code
  path. Same bug class, out of scope for this card (scoped to
  `import_page.go`), not confirmed which tables a given import plugin
  actually touches. Raised as a new Backlog card rather than expanding
  this diff.
- "main till" (manual) vs. "primary till" (this key's own English string)
  terminology mismatch — pre-existing across every sibling key, already
  logged once in the registers/locations review; this key is consistent
  with its siblings, just makes the existing nit one instance wider.

## Verified beyond automated tests

- Full gate: `gofmt -l` clean, `go build ./...`, `go vet ./...`,
  `golangci-lint run ./...` (0 issues), `go test ./...` (non-race, all
  packages green — see note below on `-race`), `guard-i18n.sh`,
  `guard-data-access.sh`, `guard-help-topics.sh`,
  `guard-compliance-claims.sh`, `guard-docs-shots.sh` all green.
- `go test ./... -race` was attempted twice (full suite, then
  `internal/pages` alone with a 15-minute timeout) and both runs hit the
  test framework's timeout on **unrelated** packages/tests
  (`internal/plugins`'s WASM JIT compile, and separately an i18n-JSON
  decode inside an unrelated `internal/pages` test,
  `TestMenuPage_ManagerOnlyTilesGatedByRole`) — not a data race report,
  not anything touching this diff's files. `-race` amplifies wall-clock
  heavily in this sandbox and isn't part of `CLAUDE.md`'s own "Before
  committing" checklist (`go build`/`go test`/`golangci-lint`, no `-race`
  flag). Treated as a pre-existing environment characteristic, not a
  regression from this change; not chased further.

## Safe to merge

Yes, after both blocking findings were fixed and the full gate re-run
clean.
