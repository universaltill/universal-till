# Code review: demo catalogue seeding becomes opt-in; setup wizard gains a shop-type step

**Card:** universaltill/ut-docs#539 (bug, p1, vertical, source:user, complexity: hard)
**Branch:** `pipeline/539-demo-seed-opt-in`
**Date:** 2026-08-12
**Complexity:** hard — Dev via a Fable subagent, Review via an independent Opus
subagent (isolated worktree). One review round; it found one merge-blocking
housekeeping gap (stale manual screenshots) and ten non-blocking findings, two
of which were fixed in this diff and two spun into follow-up cards. No
money/tax/data-loss/security-class blocker survived triage.

## What shipped

Every fresh `universal-till` install used to seed a 50-item grocery demo
catalogue unconditionally — baked into the append-only `001_init.sql` — so a
café, barber or hotel got a convenience-store catalogue on day one
(ut-docs#539's field report: a German café install came up pre-loaded with
Coca-Cola/Nestlé/Kellogg's-branded groceries). This makes it opt-in:

- **`internal/data/seeddata/`** (new package) — the demo catalogue extracted
  byte-identical from the post-023/031 migrated state (i.e. with the corrected
  EAN-13 check digits, not 001's originally-broken ones) into an embedded
  `//go:embed` SQL asset (`demo_catalogue.sql`), plus the ID lists
  (`demo_ids.sql`) and the untouched-only removal script (`remove_demo.sql`)
  shared verbatim with the migration below.
- **Migration `036_demo_seed_opt_in.sql`** — adds `items.is_sample_data`, then
  runs the removal script: on a brand-new install this fires microseconds
  after 001, before the operator ever sees the till, so every demo row is
  unconditionally gone (nothing could have referenced them yet); on an
  existing install it removes only demo items with zero references in
  `sale_lines`/`stock_movements` (directly or via a variant), and only removes
  a demo category/brand once nothing remaining references it.
  `tax_codes`/`stock_locations` are structural defaults, not demo data —
  untouched.
- **`internal/data/demo_seed_repo.go`** (`DemoSeedRepo`) — `SeedDemoCatalogue`
  (idempotent, `INSERT OR IGNORE` throughout, one tx), `RemoveDemoCatalogue`
  (reuses the same untouched predicate, reports removed vs kept), both pinned
  to a single `*sql.Tx` so the scripts' TEMP tables survive across statements.
- **Setup wizard** — new step (shop type: café/retail/service trade/
  hospitality/market stall/other, straight from `ADR-0026`'s taxonomy; sample-
  data checkbox, default **unchecked**) between shop name and PIN. `shop_type`
  is validated server-side against the six real values (a raw POST of
  anything else is dropped, not persisted). The demo seed runs last and
  best-effort — a failed seed logs and never blocks first-boot completion.
- **Settings** — shop-type field (editable later) and a manager-gated
  "Remove sample data" action reporting removed/kept counts.
- **Catalog list** — a SAMPLE badge on `is_sample_data` rows.
- **i18n** — ~20 new keys in en/ar/fa/tr (the homelab Ollama translator wasn't
  reachable from this sandbox; ar/fa/tr are the Dev subagent's own first-pass
  AI translations, independently spot-checked by the reviewer — see below).
- **Manual** — `web/help/{en,ar,fa,tr}/{users,display,catalog}.md` updated;
  screenshots regenerated (see Findings, B1).
- Deliberately **out of scope**, by design: shop-type-*matched* sample
  catalogues (ships ONE generic starter set regardless of chosen type, with
  copy that says so honestly); ADR-0026's address capture / eager cloud
  registration / plugin-suggestion tie-in; anything in `ut-cloud`.

## Independent review (Opus, isolated worktree)

Ran the full gate itself (`go build`, `go vet`, `go test ./...`, all five
CLAUDE.md-mandated guards — all green), then went file-by-file on the
highest-risk part of the change:

- **FK completeness of the "untouched" predicate is total.** Enumerated every
  FK to `items`/`item_variants` across all 36 migrations by hand. Non-
  cascading (would fail or orphan): `inventory`, `price_history`,
  `sale_lines`, `stock_movements` — the script clears the first two
  explicitly (item- and variant-scoped) and guards the last two the same way.
  Cascading (ride along safely): `item_barcodes`, `item_images`,
  `item_variants`→`variant_barcodes`, `shortcut_buttons`, `related_items`,
  `item_modifiers`, `item_station_routes`. `promotions` has no item FK at all.
  Nothing missed.
- **TEMP-table connection pinning is correct.** Migration runner executes the
  whole migration on one `*sql.Tx`; `DemoSeedRepo.RemoveDemoCatalogue` runs
  both scripts and both counts on one `*sql.Tx` too — no `database/sql`
  connection-switching hazard.
- **FK enforcement is genuinely on** (`_pragma=foreign_keys(1)` in the DSN,
  confirmed empirically by a real `787 FOREIGN KEY constraint failed` during
  the TDD re-verification below, not a silent orphan).
- **Seed asset is a faithful copy of 001's seed** — diffed all 12 shared
  columns across all 50 items, zero differences; row counts match 001 exactly
  across all 10 seeded tables.
- **Setup wizard**: `shop_type` validated server-side (not just constrained by
  the `<select>`); demo seed runs last and its failure never blocks first-boot
  (proven by a test using deps where the catalogue tables don't exist at all).
- **Settings gating**: both new routes call `isManagerOrAuthOff` first, same
  gate every other store-level mutation in the file uses — no ungated
  destructive path.
- **Translations are genuine** — all ~20 new keys in ar/fa/tr are real,
  register-appropriate, no English left untranslated, every `%d` placeholder
  present and correctly positioned in all four locales. Treat as a solid first
  pass needing native-speaker polish (the homelab Ollama model wasn't
  reachable from either sandbox this session ran in), not a blocker.
- **Copy honours the "one generic catalogue" constraint** — `setup.demo_data.
  hint` explicitly says "the same set whichever shop type you choose" in all
  four locales; nothing implies shop-type tailoring that doesn't exist.
- **The two recurring bug classes this pipeline keeps finding** — a file-write
  handler missing `os.MkdirAll`, a cwd-relative path where `paths.Data(...)`
  belongs — don't apply; this diff is DB-only plus one new `e2e/seed_demo`
  helper that already follows `e2e/seed_faq`'s existing convention.
- **No real client/shop name** anywhere (Test Cafe / Corner Shop / fictional
  demo customers only).

### TDD re-verification (done personally, by the reviewer, in its isolated worktree)

Picked `TestDemoCatalogueUpgradeKeepsTouchedItems` (the "a sold demo item
survives the removal migration" claim) plus its data-layer twin
`TestRemoveDemoCatalogue`. Deleted the two `sale_lines` guard clauses from
**both** `remove_demo.sql` and its mirror inside `036_demo_seed_opt_in.sql`
(both, so the drift guard wouldn't fire and confound the result). Result:
`FOREIGN KEY constraint failed (787)` on both tests — not a silent orphan, a
hard failure, which independently proves FK enforcement is live during
migrations and that without the guard a real shop's till would refuse to open
after upgrade. Restored both files; all tests pass again.

### Findings

**B1 — blocking, FIXED. `guard-docs-shots.sh` failed.** The manual's
screenshot-freshness guard is wired into CI
(`.github/workflows/ci.yml`) and compares content hashes of `web/ui/**`,
`web/public/**`, non-test `internal/pages/**.go`, and each topic's markdown
against `web/help/img/manifest.json` — a global surface hash, so *any*
`internal/pages/**.go` change invalidates every topic's screenshot, not just
the touched ones. The Dev subagent had no browser in its sandbox and
correctly said so rather than silently skipping it. This session's own
sandbox *does* have a pre-installed Chromium
(`/opt/pw-browsers/chromium-1194`), one revision behind what this repo's
pinned `@playwright/test` (1.61.1) expects — `npx playwright install` tries
to download the matching revision and the network policy here blocks that
download (`403 request rejected: host not permitted`). Worked around by
pointing `playwright.docs.config.ts`'s `launchOptions.executablePath` at the
pre-installed binary — **only inside an isolated `git worktree`**, run there,
then copied `web/help/img/**` + regenerated `manifest.json` back and reverted
the config change (never committed) — so the temporary path never lands on
`main`. Also caught and fixed in the process: `e2e/tests-docs/docs-shots.
spec.ts`'s `ensureOperator()` walked the wizard assuming the old 5-step
layout and timed out on 12 of 68 shots (`users`/`translations`/
`kitchen-stations` × 4 locales) — the new shop-type step sits between "shop
name" and "PIN" and needed one more `Next` click, mirroring the fix already
present in `e2e/tests/login.spec.ts`. Full 68/68 pass after the fix;
`guard-docs-shots.sh` now green.

**N1 — non-blocking, spun into ut-docs#566.** The "untouched" predicate
checks trading history (`sale_lines`/`stock_movements`) but not edits: a shop
that renames/reprices a demo item before its first sale would have it (and
its barcode/variant/image, `ON DELETE CASCADE`) silently deleted on the next
upgrade or a manual "Remove sample data" click. Correct to the card's stated
acceptance criteria as written (sold/stock-adjusted only) — a spec gap, not
an implementation defect — but real enough to file. Recommended fix in the
follow-up: an exact "still pristine" check against the known seeded
`name`/`sku`/`base_price` (the seed asset already knows these), stronger than
a generic `updated_at` heuristic and no schema change needed.

**N2 — non-blocking, spun into ut-docs#567 (`needs-info`, posted to the
product owner).** "Sample data" in Settings is catalogue-only by design, but
001 also unconditionally seeds 3 fictional customers and 3 **live, working**
promo codes (`PROMO50`, `PROMO500`, `DISC10` — a real 10%-off-basket code),
neither touched by the new opt-in/removal mechanism. An owner who taps
"Remove sample data" will reasonably expect this to cover them — it doesn't.
Whether to extend the same opt-in treatment to promotions/customers is a
real (if small) revenue-leak-vs-scope business call, not an engineering
default — routed to the product owner rather than guessed past.

**N3 — FIXED in this diff.** `TestMigration036MatchesSeedData` only caught
drift in one direction (an ID present in `seeddata.ItemIDs` but missing from
the SQL). Added the reverse check: an item row added to `demo_catalogue.sql`'s
INSERT block without also adding it to `ItemIDs`/`demo_ids.sql` would seed
fine but be permanently unremovable, and nothing previously caught that.

**N4 — FIXED in this diff.** `internal/pages/demo_seed_opt_in_test.go`'s
removal-response assertion was `!strings.Contains(body, "49") ||
!strings.Contains(body, "1")` — the `"1"` half is trivially satisfied by the
`1` inside `49` and asserts nothing. Tightened to the actual rendered
strings (`"Removed 49 sample item"` / `"1 could not be removed"`).

Accepted as-is, not fixed (all genuinely minor, none change behaviour):

- **N5** — the Settings "This till has N sample item(s)…" line doesn't
  live-update after a removal (the HTMX swap only targets the result
  message). Cosmetic, page reload shows the right count.
- **N6** — the removal handler's error path writes raw `err.Error()` into the
  panel, untranslated. Pre-existing house style in the same file (three other
  call sites do the same), not a regression this diff introduced.
- **N7** — `check-lang-pack-drift.sh` will go red on `main` after merge for
  the 20 new keys, until the external `ut-plugin-language-*` packs catch up.
  By design not a PR gate (`lang-pack-drift.yml` deliberately excludes
  `pull_request`). Flagged for the pack repos' owners, not this PR's job.
- **N8** — minor i18n register nits (Arabic uses two different words for
  "till" across new keys where the rest of the corpus uses one; a mild
  Turkish anglicism). Real but small; folded into the "first pass, needs
  native-speaker polish" caveat already true of every locale here.
- **N9** — `e2e/run-till.sh` now seeds the demo catalogue unconditionally for
  every e2e project, including the "genuinely fresh install" one, so
  `login.spec.ts` can assert the checkbox defaults unchecked but not that a
  fresh install actually ends with an empty catalogue end-to-end. The Go-level
  migration tests (`TestDemoCatalogueRemovedOnFreshInstall`, re-verified
  above) do cover that claim directly; the e2e gap is real but narrow.
- **N10** — the two-pass category delete (children, then roots) is exactly
  right for the current 4-root/6-child demo tree but would need a third pass
  for a hypothetical 3-level category. Worth a comment for future readers;
  not a bug today.

## Verified beyond the automated suite (this session, orchestrator)

- **Real driven run, not just rendered-HTML-string assertions.** Booted a
  fresh till (`go build`, run the binary from a throwaway data dir — the same
  pattern `e2e/run-till.sh` uses) and drove it with Playwright, screenshots
  actually looked at:
  - Setup wizard's new shop-type step in **English/LTR** and **Farsi/RTL** —
    checkbox correctly unchecked by default, select/checkbox/button order and
    alignment correct in both directions, dropdown arrow and dot-indicator
    correctly mirrored under RTL.
  - Completed the wizard with the sample-data checkbox checked → **Catalog**
    page showed all 50 seeded items with a clean SAMPLE badge, no layout
    overlap.
  - **Settings** page showing the new Shop Type card and the Sample Data card
    ("This till has 50 sample item(s)…") in the same driven session.
  - Clicked **Remove sample data** for real → catalog went to "No items",
    proving the removal path works end-to-end, not just in isolated repo
    tests.
  - Did **not** separately drive dark theme or the 10-inch kiosk viewport for
    this change — the touched surfaces (a settings card, a wizard step, a
    list-row badge) are ordinary form/table content with no kiosk-specific or
    theme-specific styling introduced, and the regenerated manual screenshots
    (below) cover the same surfaces at the reference viewport across all four
    locales.
- **Official manual screenshots regenerated for real** (`make docs-shots`'s
  equivalent, run in an isolated worktree — see B1): 68/68 pass, 17 topics ×
  4 locales, `guard-docs-shots.sh` green. Spot-checked `web/help/img/en/
  catalog.png` directly — the SAMPLE badge renders correctly in the actual
  committed manual screenshot, not just my own ad hoc verification script.
- **TDD claim independently re-verified twice** — once by the reviewer
  subagent (see above, `787 FOREIGN KEY constraint failed` on both target
  tests with the guard clauses removed), once implicitly by this session
  fixing N3's reverse-drift gap and confirming it fires on a deliberately
  mismatched asset before restoring.
- `go build ./...`, `go vet ./...` — clean, re-run after every fix.
- Full `go test ./... -race -count=1` — 0 failures, re-run after the N3/N4
  fixes and again after the docs-shots regeneration.
- All six guards green: `guard-data-access.sh`, `guard-i18n.sh` (936 keys, all
  locales match en.json), `guard-help-topics.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-docs-shots.sh`.
- Manual (`web/help/`) updated in the same branch for every surface a shop
  owner sees or does that changed — wizard step, Settings sections, catalog
  badge — in all four locales, screenshots fresh (not stale, see B1).
- No real client/shop name, no secret-shaped literal, anywhere in the diff.

## Safe-to-merge verdict

**Yes.** The highest-risk part of the change — the demo-removal migration —
was reviewed line-by-line against every FK in the schema and independently
proven to fail loudly (not orphan silently) rather than run without its
safety guard, by an actual TDD revert. The one blocking finding (stale manual
screenshots) is fixed with the manual freshly regenerated and spot-checked.
Both non-blocking findings worth a card (N1, N2) are filed
(ut-docs#566, ut-docs#567 — the latter routed to the product owner as a real,
small business call rather than guessed past). Scope was deliberately
narrowed by the Architect step from ut-docs#539's full ask — shop-type-
matched catalogues and the rest of ADR-0026 are explicit, disclosed
follow-ups, not silently dropped work.
