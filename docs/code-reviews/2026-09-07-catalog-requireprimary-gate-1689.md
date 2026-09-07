# Code review: catalog item/variant/barcode requirePrimary gate

- **Card:** universaltill/ut-docs#1689 — "Catalog mutation handlers
  (items/item_variants/item_barcodes/variant_barcodes) have no
  requirePrimary gate despite syncing via adminTables"
- **Repo:** `universal-till`
- **Complexity:** medium (Dev at Sonnet, Review at Opus per
  `scrum-master`'s model-routing table)
- **Reviewer:** independent fresh-context Opus subagent, isolated
  worktree, did not see the implementation reasoning — read the diff
  cold, ran the repo's own guards live, and independently reproduced the
  TDD claim by reverting the gate on one route and confirming the new
  test fails.

## What shipped

`internal/pages/catalog/handlers.go`'s `requirePrimary` closure (added by
ut-docs#1667 for `item_modifier_groups`/`item_modifier_options`) is now
parameterized to take a message key, and is applied to 10 previously
ungated mutation routes that write to `items`/`item_variants`/
`item_barcodes`/`variant_barcodes` — all four have been synced shop-wide
via `sync_admin_repo.go`'s `adminTables` for a long time, but a write
accepted on a satellite till was silently reverted on the next admin
pull (the same bug class ut-docs#1546/#1590/#1667 already fixed for
other tables):

- `POST /api/catalog/item` (create), `/item/update`, `/item/deactivate`
- `POST /api/catalog/item-cost`, `/api/catalog/item-lead-time`
- `POST /api/catalog/variant` (create/update), `/variant/deactivate`
- `POST /api/catalog/barcode` (attach), `/barcode/delete` (detach)
- `POST /api/catalog/barcode-backfill` (bulk commit — the `GET` preview
  beside it is a read-only dry run and is deliberately NOT gated)

A new key `catalog.error.item_replica_use_primary` was added to
`web/locales/{en,ar,fa,tr}.json` rather than reusing the existing
`catalog.error.replica_use_primary` ("manage customization options"),
which reads wrong for an item/variant/barcode edit. Matching German and
Spanish translations were added in `ut-plugin-language-de`/`-es` in the
same cycle (see their own review records) to avoid landing `main` red on
`lang-pack-drift`.

New regression test `internal/pages/catalog/item_replica_gate_test.go`
(`TestCatalogItemMutations_RefusedOnReplica`) exercises every gated
route against a replica, asserting the 409 status, the exact localized
message, and that nothing changed in the DB — plus a positive check that
the `GET` backfill preview still returns 200 on a replica.

The user manual (`web/help/{en,ar,fa,tr}/catalog.md`'s "Good to know"
bullet) is updated in all four locales to describe the broadened
behaviour (item/variant/barcode/cost/lead-time, not just customization
options), and `web/help/img/manifest.json` was regenerated via
`make docs-shots` — no screenshot pixels actually changed (the bullet
isn't part of any captured page), only the topic-markdown hash.

## Review findings

**Correctness (traced independently):** every route that writes to the
four synced tables is now gated; `item/image` and `variant/image` are
correctly left ungated (they write only `item_images`, explicitly
excluded from `adminTables` — "files don't travel"); the `GET`
barcode-backfill preview is correctly left ungated (read-only). No gap
found in route coverage.

**Fixed during review:**
- Doc: the manual's "Good to know" bullet described only customization
  options; broadened to cover the new gated surface, in all four
  locales, screenshots regenerated.
- Nit: `item/image`/`variant/image` now carry an explicit comment
  explaining why they're deliberately ungated, so a future diff doesn't
  read them as a miss.
- Nit: `item-cost`/`item-lead-time` now gate before `ParseForm`, matching
  every other gated route's ordering (was harmless either way — no write
  precedes the gate in either order — but inconsistent).
- Test: `item_replica_gate_test.go` assertions changed from `t.Fatalf` to
  `t.Errorf` so one broken route doesn't mask the other nine in a single
  run; added a dedicated `variant_barcodes` attach case (barcode attach
  to a *variant*, not just an item) since that table has its own write
  path (`AddBarcode` routes to `variant_barcodes` when `variantId` is
  set) that the original test only covered via route identity, not an
  actual attempted write.

**Deferred — new Backlog cards, out of scope for this ticket** (same bug
class, different surface, larger blast radius in one case):
- `internal/pages/import_page.go`'s `POST /api/import` bulk-creates
  items/attaches barcodes with no `requirePrimary` gate at all — a
  replica import would silently revert on the next admin pull, worse
  than one row.
- `internal/pages/buttons_api.go`'s `/api/buttons/{reorder,add,remove}`
  mutate `shortcut_buttons`, which **is** in `adminTables`, with no gate.

## Verified, live, not just read

- `gofmt -l .` — no output.
- `go build ./...`, `go vet ./...` — clean.
- `golangci-lint run ./...` — `0 issues.`
- `go test ./...` — full suite green, no failures (`internal/pages`
  185–192s, `internal/data` ~52s, `internal/pages/catalog` ~2.2s).
- `bash scripts/ci/guard-data-access.sh` — no inline SQL outside
  `internal/data`/`internal/db` (the test file's raw SQL is test-file
  exempt, per the guard's own scope).
- `bash scripts/ci/guard-i18n.sh` — 1451 template keys resolve, all
  locales match `en.json`.
- `bash scripts/ci/guard-docs-shots.sh` — 25 routed topics × 4 locales
  fresh after `make docs-shots`.
- `bash scripts/ci/guard-help-topics.sh`,
  `bash scripts/ci/guard-compliance-claims.sh` — clean.
- `bash scripts/ci/guard-deadcode-baseline.sh` fails in this environment
  (missing GTK/WebKit headers, `cmd/unitill-desktop`/`webview_go`) —
  confirmed via `git stash` that this reproduces identically on `main`
  before this change; pre-existing environment gap noted in this repo's
  own `CLAUDE.md` (ut-docs#1581), not caused by this diff.
- **TDD re-verification (independent):** removed the gate from
  `POST /api/catalog/item` only, ran
  `go test ./internal/pages/catalog/... -run TestCatalogItemMutations_RefusedOnReplica -v`
  → failed with `want 409, got 200`, body showing the item's row was
  really created; restored the gate, re-ran → passed. Confirms the test
  is a genuine regression guard, not a tautology.
- Confirmed the 409 body actually reaches the operator: `catalog.html`
  has an `htmx:responseError` listener on `document.body` that renders
  `xhr.responseText` into the page notice, same as ut-docs#1667.
- Checked the two recurring bug classes this pipeline watches for
  (missing `os.MkdirAll`, cwd-relative path instead of `paths.Data`) —
  neither applies; this diff writes no files, and the three existing
  file-writing handlers in this file were re-checked and are unaffected.
- No real client/shop name, no secret-shaped literal — fixtures are
  `http://primary.example` (RFC 2606), `Flat White`/`COFFEE`.
- Git identity on the commit: `Farshid Mirza
  <4035824+farshidmirza@users.noreply.github.com>`.

## Verdict

**Safe to merge.** No blocker-class findings. All non-blocker findings
from the independent review were fixed in this same branch before
merge; the two deferred findings (F2/F3 above) are the same bug class on
different, out-of-scope surfaces and are tracked as new Backlog cards
rather than silently left unrecorded.
