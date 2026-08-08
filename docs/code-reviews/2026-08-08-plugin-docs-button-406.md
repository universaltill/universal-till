# Code review: per-plugin documentation button (ut-docs#406)

**Date:** 2026-08-08
**Card:** ut-docs#406 — "Per-plugin documentation, opened from a button on the installed plugin"
**Design:** ut-docs ADR-0037 (merged, `universaltill/ut-docs#466`) — plugin docs
ride the existing plugin-page mechanism via a reserved `key: "docs"` page entry.
**Repos touched:** `universal-till` (core mechanism), `ut-plugin-tax-uk` (reference implementation, AC7)
**Model routing:** `complexity:hard` — dev (a prior, since-abandoned cycle) at
an unrecorded model; this cycle resumed the stale branch inline (Sonnet) to
finish it; independent review at **Opus**, fresh-context subagent, per the
scrum-master skill's routing table.

## What shipped

- `internal/pages/plugins_page.go`: `/plugins` now resolves, per installed
  plugin, whether it has an active `type:"page"` entry keyed `"docs"`, and
  passes that entry's route to the template as `docsRoute`. A plugin with
  no such entry (or a disabled plugin/entry) gets an empty route — no
  button, never a button to an empty page.
- `web/ui/pages/plugins.html`: a Docs button per plugin card, shown only
  when `docsRoute` is non-empty; opens the existing in-till `/plugin/...`
  render path — never an external link, works with the network down.
- `web/locales/{en,ar,fa,tr}.json` + `web/help/{en,ar,fa,tr}/plugins.md`:
  new locale key and manual note, in the same branch as the feature
  (standing product-owner rule, 2026-08-06).
- `internal/pages/plugins_page_test.go`: 4 tests pinning the docsRoute join
  (active entry surfaces; no entry / inactive entry / disabled plugin all
  surface none).
- `ut-plugin-tax-uk` (reference implementation, AC7): a `docs` page entry
  in `manifest.json`, `content/index.html` (static HTML — prose
  documentation, not FAQ-style Q&A, per the ADR's "Dev's call"), and
  `scripts/package.sh` updated to actually include `content/` in the
  shipped artifact (it silently didn't before).

## Independent review (Opus, fresh context)

Ran the full gate itself rather than trusting the diff: `go build`/`vet`,
`go test ./internal/pages/...`, all four `universal-till` CI guards
(`guard-data-access`, `guard-i18n`, `guard-help-topics`,
`guard-docs-shots`), and `ut-plugin-tax-uk`'s own `build.sh`/`validate.sh`/
`package.sh`, confirming the packaged tarball actually contains
`content/index.html`. Mutation-tested the 4 new Go tests (temporarily
broke the join condition and the active/enabled filter; confirmed each
test fails for the reason it claims to) rather than trusting them by name.
Traced the plugin-content trust chain end to end (artifact SHA256 → ent
`ArtifactHash` cross-check → Ed25519 manifest signature) to confirm AC4's
"no sanitisation needed" is a reasoned, ADR-recorded decision, not a
silently skipped requirement.

### Blockers found — both fixed in this cycle

1. **A `docs` page entry also injected a tile into `/menu`.** Every
   `type:"page"` entry (including `docs`) fed
   `internal/plugins.Manager.loadMenuEntries` → `common.BuildMenu` →
   `/menu`'s touch-tile launcher — undesigned, untested, unmentioned in
   ADR-0037. Installing `ut-plugin-tax-uk` would have added an
   unlocalized, no-icon tile reading "How the eat-in/takeaway VAT switch
   works" to the till's primary navigation screen, and the effect scales
   with every plugin adopting the `docs` convention.
   **Fix:** `internal/plugins.DocsEntryKey` exported constant;
   `loadMenuEntries` now skips it explicitly (both call sites — `Init` and
   `Reload` share the one function). `plugins_page.go`'s existing
   docs-lookup switched to the same constant so the two can't drift.
   Regression test `TestLoadMenuEntriesExcludesDocsEntry` (fresh
   `manager_test.go`, mutation-style: asserts BOTH that a `docs` entry is
   excluded AND that an ordinary page entry in the same plugin still
   surfaces, so the fix can't be a blanket "never build any menu entry"
   regression).
2. **Wrong navigation instruction in the reference doc content.**
   `ut-plugin-tax-uk/content/index.html` told a shop owner to find a tax
   code's ID under "Settings → Tax Codes" — no such screen exists; the
   only tax-code UI is the `Tax code` field on the catalog item form
   (`web/ui/pages/catalog.html`, `catalog.tax_code`). For a feature whose
   whole premise is trustworthy in-till documentation, shipping a wrong
   instruction in the reference implementation would have undermined the
   point. **Fix:** reworded to point at Catalog → item → Tax code field.

### Non-blocking, triaged

- **Reserved-key menu-namespace collision** (`MenuPlugins` keyed by entry
  key without plugin id) is pre-existing and already flagged as "Not
  decided here" in ADR-0037 itself — filed as a new Backlog follow-up
  rather than fixed inline here (out of this card's scope; the collision
  risk is unchanged by this diff, only made marginally more likely as
  `docs` gets adopted).
- **Static-HTML docs have no locale-fallback notice** (only the
  `content/<locale>.json` bundle path shows "you're seeing a different
  language" — `content/index.html` doesn't, and ADR-0037 explicitly left
  the format choice to the implementer). Accepted as-is for this English-
  only plugin; noted for whoever ships the first non-English reference.
- **`validate.sh` didn't guard a declared `docs` entry actually having
  content** — fixed inline (3-line addition) since it's cheap and directly
  closes the exact "broken affordance" AC3 forbids on any future edit to
  this plugin.
- **ADR number 0037 was double-claimed** by an unrelated, unmerged,
  already-superseded branch (`pipeline/adr-0037-azure-scheduled-monitors`,
  the Azure Container Apps monitoring proposal ut-docs#447's own later
  comments moved away from in favour of a NAS-based design). Not this
  card's branch to fix; flagged on ut-docs#447 instead.
- Manifest `route` strings are stored/rendered with no format validation
  (a `javascript:` route would render as a live link in `plugins.html`'s
  `:href`) — acceptable inside the ADR's Ed25519-verified-artifact trust
  model, but cheap to harden; left as a follow-up, not a blocker.
- Pre-existing `gofmt -l` drift in 4 unrelated files, none touched by this
  branch — confirmed out of scope, not fixed here.

## Verified beyond the automated suite

- Seeded a real `docs` page entry + `content/index.html` matching the
  actual packaged tarball layout into a real till DB/filesystem
  (`internal/pages` test harness) and confirmed `GET /plugin/tax-uk-docs`
  renders the real content end to end.
- Packaged `ut-plugin-tax-uk` for real (`scripts/package.sh`) and
  confirmed `content/index.html` is present in the resulting
  `.tar.gz` — the actual proof the reference implementation ships
  correctly, not just that the files exist on disk.
- `bash scripts/ci/guard-docs-shots.sh` regenerated for real
  (`make docs-shots` via a local, uncommitted Playwright
  `executablePath` override for this sandbox's pre-installed Chromium
  build — never committed, reverted before every commit on this branch)
  — 60/60 specs pass, all 4 locales.
- Full `go test ./...` green; no pre-existing failures reproduced.

## Safe-to-merge verdict

Yes, after the two blocker fixes above. All CLAUDE.md guards green in both
repos, mechanism verified end to end through a real packaged artifact, and
the one previously-undiscovered design gap (menu leak) is now closed with
a test that would catch a regression.
