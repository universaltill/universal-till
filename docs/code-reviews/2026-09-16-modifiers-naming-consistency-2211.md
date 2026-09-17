# Review: Modifiers naming consistency (ut-docs#2211)

**Branch:** `fix/2211-modifiers-naming-consistency`
**Reviewer:** Sonnet 5, same-model, same-session independent pass (see note below)
**Verdict:** PASS — safe to merge once the pipeline's standing auto-push
authorization is confirmed (blocked on `ut-docs#2277`, not on this diff).

## Independence note (read this first)

`MODEL-ROUTING.md` calls for a `complexity:medium` card to be reviewed by
Opus in a fresh, independent context. This cycle tried that twice:
`mcp__Claude_Code_Remote__create_session` (model `claude-opus-5`, this
branch checked out) failed three consecutive times with `"the parent
session's permission mode is not yet available... retry, or run the
parent in auto mode"` — not a transient blip, the identical error on
every attempt across several minutes — and no in-session subagent/`Task`
tool was exposed to this runtime to fall back to (checked via
`ToolSearch`; only `EnterWorktree`/`ExitWorktree` for worktree isolation,
no `Agent`/`Task` spawn primitive, despite `Agent` appearing in the
parent session's own `turn_handoff.tools` metadata — inconsistent with
what was actually callable). So this review is **not** the genuinely
independent, different-model pass the process calls for — it is the same
session, same model, doing a second, deliberately skeptical pass with
real re-execution (build/vet/test/guards, plus two live
revert-then-restore TDD checks) rather than re-reading its own diff and
agreeing with itself. That is a real gap against the standing process,
not a formality — flagged explicitly in the cycle's final report for the
pipeline owner, not glossed over here.

## What shipped

`ut-docs#2211` asked for "Modifiers" to be a top-level catalog tab, peer
to Variants. Investigation this cycle (recorded on the issue) found that
requirement already shipped by `ut-docs#1957` — `/modifiers` is a
declared `uislot.CoreItems` rail entry, one tap from `/items`. The real,
current gap: the rail label says **"Modifiers"** but the page you land on
titled itself **"Customization options"** — plus the same stale
"customization group(s)/options" wording leaking into ~10 more locale
strings and the catalog help topic, and a hardcoded English `<title>`
literal in `internal/pages/catalog/handlers.go` that PR #1189's
`internal/pages/*.go`-only (non-recursive) sweep missed because this
handler lives one directory deeper (`internal/pages/catalog/`).

This diff:

- Renames every "customization group(s)/options" user-facing string to
  "Modifiers" in `web/locales/{en,ar,fa,tr}.json` (11 keys), reusing each
  locale's own already-established term for "Modifiers"
  (`items.modifiers.name`: ar `الإضافات`, fa `افزودنی‌ها`, tr
  `Modifikasyonlar`) rather than inventing a new one — and, in the same
  diff, fixes `catalog.modifiers.manage`/`catalog.modifiers.summary_none`,
  which were literal untranslated English copy-paste in `ar.json`,
  `fa.json` **and** `tr.json` (not just stale wording — never translated
  at all).
- Fixes the hardcoded `"title": "Customization options"` Go literal in
  `handlers.go` to `httpx.T(httpx.RequestLocale(r), "modifiers.title")`,
  the same pattern `ut-docs#2297`/PR#1189 established elsewhere in this
  file for the identical bug class.
- Updates the catalog help topic (`web/help/{en,ar,fa,tr}/catalog.md`) to
  match, keeping each locale's existing bullet/heading structure
  byte-identical in shape so `guard-help-drift.sh` introduces no new
  tracked drift (verified — see below).
- Adds `TestCoreItems_ModifiersIsTopLevel` (`internal/uislot/slot_test.go`)
  pinning `/modifiers` as a declared top-level `CoreItems` entry — the
  original card's AC#1, previously unpinned by any test — and
  `TestModifiersPage_HeadingMatchesRailLabel`
  (`internal/pages/catalog/modifiers_shop_page_test.go`) pinning the
  page's own `<h1>` against drifting away from the rail label again.
- Fixes three pre-existing tests whose literal-string assertions would
  otherwise now fail on the renamed copy:
  `modifiers_shop_page_test.go`'s empty-state check, `modifiers_admin_test.go`'s
  replica-banner message check, `category_inherit_2284_test.go`'s
  no-groups-yet check, and one e2e spec assertion
  (`items-shell-catalog-top-actions-rail-2090.spec.ts`).
- Regenerates `web/help/img/manifest.json` for the `catalog` topic only —
  **not** a full `make docs-shots` re-render. See below for why, and what
  was verified about it.

Deliberately **not** in scope: the item-editor's "Manage customization
groups" nested dialog still mixes full inline group CRUD with an
attach-existing-group dropdown; the product owner's fuller 2026-09-16 ask
("must be only a multi-selection combo") describes replacing that with an
attach/detach-only widget, moving all CRUD onto `/modifiers`. That's a
real interaction redesign sitting directly on top of `#2284`'s
still-fresh (merged same day) category-inheritance/opt-out UI, and two
other PRs were open against this exact screen area at pick time
(`#1197`, `#1200`). Filing that as a separate follow-up card rather than
forcing it into this diff is the right call — see `BUILD-CYCLE.md`'s
"what one item is allowed to cost."

## What was verified independently (not just re-read)

- `go build ./...`, `go vet ./...` — clean.
- `gofmt -l` on every touched `.go` file — clean.
- `golangci-lint run ./internal/uislot/... ./internal/pages/catalog/...`
  — 0 issues.
- `go test ./internal/uislot/... ./internal/pages/catalog/...` — green.
- **Full** `go test ./...` (not just the touched packages) — green,
  every package, no skips introduced.
- `guard-i18n.sh`, `guard-help-topics.sh`, `guard-help-drift.sh`,
  `guard-docs-shots.sh`, `guard-data-access.sh`,
  `guard-compliance-claims.sh` — all green. `guard-help-drift.sh`'s
  output was diffed before/after this branch's help-topic edits
  (`git stash` the four `catalog.md` files, re-run, compare) — byte-
  identical drift-entry list, confirming the rewritten paragraphs
  introduced no new tracked structural drift.
- **TDD claim 1, live revert/restore**: reverted
  `internal/pages/catalog/handlers.go`'s title fix (`git stash` on that
  file + `en.json`), re-ran
  `TestModifiersPage_HeadingMatchesRailLabel` — real assertion failure
  (`expected the page heading to say "Modifiers"...`), not a compile
  error. Restored (`git stash pop`), re-ran — passes. Re-ran the full
  package suite after restore to confirm nothing else regressed.
- **TDD claim 2, live revert/restore**: temporarily removed the
  `/modifiers` row from `internal/uislot/slot.go`'s `CoreItems` table,
  re-ran `TestCoreItems_ModifiersIsTopLevel` — real assertion failure
  (`Modifiers must be a declared top-level Items-slot entry`). Restored,
  re-ran — passes, `git diff` on `slot.go` empty (clean round-trip).
  This is a regression pin on pre-existing (not newly-built) behavior,
  so also checked it isn't vacuously true: the assertion checks both
  `LabelKey` and `Href`, not just presence, and the revert test above
  proves it actually fails when the entry is absent.
- **docs-shots regeneration scope**: a full `make docs-shots` run was
  attempted first and produced changes to all 31 topics × 4 locales (113
  PNGs) — a known Chromium-version mismatch
  (`docs-shots: WARNING — reused Chromium version does not match the
  @playwright/test pin`: reused 141.0.7390.37 vs. pinned 149.0.7827.55)
  causing font-rasterization drift unrelated to this diff, the same
  failure mode PR #1197's own review record describes handling. Verified
  `web/help/img/en/catalog.png` (and ar/fa/tr) were **byte-identical**
  to `main` despite both the markdown and the Go surface changing (`git
  status --short web/help/img/` showed nothing after a full regen, before
  the manifest rewrite) — the renamed prose isn't part of what the
  screenshot actually frames. Re-ran filtered to only the `catalog` topic
  (`npx playwright test --grep "screenshot: catalog "`) plus
  `write-manifest.js`, and confirmed via `git diff` that the resulting
  manifest change touches **only** `surface_sha256` and the `catalog`
  topic's 4 hashes — no other topic's hash, no `.png` file anywhere in
  the diff. This is the correct, minimal diff; committing the full
  113-PNG noisy regen would have been a real defect.
- Translation-quality self-check (see the independence note above for
  why this is a same-model check, not a native/independent one): each
  new ar/fa/tr string was checked against `items.modifiers.name`'s
  already-shipped term for "Modifiers" in that locale and reuses it
  consistently rather than introducing a synonym; grammar was checked by
  a second read against the sentence patterns already used elsewhere in
  the same locale files for structurally identical sentences (e.g. "No
  X yet — create them under Y first" patterns already present for other
  features). This is the review's weakest link given the independence
  gap above — worth a native-speaker or genuinely independent-model spot
  check before this is taken as final confirmation, not just this diff's
  own say-so.
- No `os.MkdirAll`/`paths.Data(...)` class of bug applies — this diff
  touches no file-write handler and no filesystem path construction.
- No real client/shop name, and no secret-shaped literal, anywhere in
  the diff (`grep -iE "api[_-]?key|secret|password|token|BEGIN
  (RSA|PRIVATE)|taskrunner|task runner"` across every changed file —
  zero matches).
- `web/help/de/catalog.md` (the German help topic, which lives in this
  core repo even though `de` UI strings are plugin-owned in
  `ut-plugin-language-de`) is **not** touched by this diff — a
  deliberate deferral to the same-cycle `ut-plugin-language-de` follow-up
  PR, named explicitly in the PR body, not a silent gap.

## Findings

None blocking. One accepted, out-of-scope observation (not fixed here,
not filed as a new card by this review — the orchestrator's own findings
already cover it): `internal/pages/catalog/handlers.go` has two more
hardcoded page-`<title>` literals ("Catalog", "Option sets") at lines
688/779, same bug class as the one fixed here, also missed by PR#1189's
non-recursive sweep, also unrelated to Modifiers — correctly left alone
by this diff since fixing them isn't this card's scope.

## Merge status

**Not merged, deliberately.** `ut-docs#2277` (Admin Review, unresolved at
review time) raises real evidence the pipeline's standing "no real users
yet" auto-push authorization may no longer hold. Per that card's own
reasoning and the precedent already set by two other PRs this same cycle
(`#1197`, `#1200`, both held unmerged for the identical reason), this
branch stays open, reviewed, CI-pending, and unmerged until a human
answers `#2277`.
