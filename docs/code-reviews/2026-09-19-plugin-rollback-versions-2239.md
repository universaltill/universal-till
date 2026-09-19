# 2026-09-19 — Wire up plugin rollback version discovery (ut-docs#2239)

## What shipped

`RollbackManager.GetVersionHistory` (`internal/plugins/rollback.go`) listed
the on-disk version snapshots available to roll a plugin back to, but had
no production caller — `POST /api/plugins/{id}/rollback` was live (used by
the multi-till sync's failed-upgrade recovery) but nothing under `web/ui`
could discover which version string to pass it. This closes that gap:

- `GET /api/plugins/{id}/versions` (`internal/pages/plugin_api.go`,
  `handleListPluginVersions`) — same `canPerform(d, r, "plugin_management")`
  gate as its sibling plugin-management endpoints, most-recent-first order,
  never leaks the on-disk snapshot path (`VersionInfo.Path` is `json:"-"`),
  never returns JSON `null` for an empty history.
- `web/ui/pages/plugins.html` — a "Versions" button per plugin card,
  fetched lazily on first open, listing earlier versions with a
  "Roll back to this version" action (confirm dialog, reuses `window.act`,
  extended to accept an optional JSON body).
- i18n: 6 new keys added to `web/locales/{en,ar,fa,tr}.json` (en is the
  base; ar/fa/tr translated directly, not machine-generated or baselined).
- Manual: `web/help/{en,de,ar,fa,tr}/plugins.md` gained a step describing
  the affordance; `de` already had full parity with `en` and stays in
  parity; `ar`/`fa`/`tr` had a pre-existing, tracked 1-step translation lag
  (`scripts/ci/i18n-baseline/help-drift-baseline.json`) — the new step
  moved the gap forward by one on both sides rather than closing or
  widening it, same convention as the two prior updates recorded in that
  same baseline entry (ut-docs#2131, ut-docs#2132). Screenshots
  regenerated via `make docs-shots`.

## Independent review

Opus, in an isolated worktree (`isolation: "worktree"`, no shared checkout
with the orchestrating session). Ran the full gate for real (not assumed):
`go build ./...`, `go vet ./...`, `golangci-lint run ./...` (0 issues),
`go test ./internal/pages/... ./internal/plugins/...`, plus
`guard-data-access.sh`, `guard-i18n.sh`, `guard-help-topics.sh`,
`guard-help-drift.sh`, `guard-compliance-claims.sh`, `guard-docs-shots.sh`,
`guard-page-http-error.sh`, `guard-kiosk-engine.sh`, `guard-htmx-loaded.sh`
— all green.

**Two should-fix findings, both fixed (not deferred):**

1. **The Versions panel misreported every fetch failure as "no earlier
   versions available"**, including a 403 (no manager session) and a
   401 (session expiry) — `common.LocalizedError` answers `text/plain`,
   not JSON, so `res.json()` on a non-2xx response threw and the
   `.catch()` silently rendered the empty state. Worse, it was sticky
   (`versions` was no longer `null`, so re-opening never retried), and
   had no 401→`/login` redirect the way `window.act` right below it
   already does. **Fixed**: the fetch now branches on `res.status === 401`
   (redirect, matching `window.act`), reads a non-`ok` response as text
   (these bodies are already-localized plain strings, not JSON envelopes —
   confirmed by reading `common.LocalizedError`'s implementation) and
   shows it via a new `vError` state that resets `versions` to `null` so
   a retry is possible.
2. **Rolling back silently destroyed the ability to roll forward again.**
   `StoreVersion` is only ever called before an *update* (snapshotting the
   version being left) — never before a *rollback*. So install 1.0.0 →
   update 2.0.0 → update 3.0.0 leaves only `{1.0.0, 2.0.0}` on disk;
   rolling back to 2.0.0 makes 3.0.0 permanently unrecoverable, because it
   was never snapshotted. This was always true of `Rollback()`, but this
   card is what makes it reachable in one tap for the first time. **Fixed**:
   `Rollback` now calls `StoreVersion` for the version it's leaving before
   switching, mirroring `handleUpdatePlugin`'s own call — but only when the
   live per-version install directory actually exists on disk first
   (`os.Stat` check), since `StoreVersion` unconditionally clears any
   existing snapshot before copying and would otherwise destroy an
   already-good prior snapshot when the live source is missing. Added
   `TestRollback_PreservesRollForwardToTheVersionItLeaves`, TDD-verified:
   reverted the fix, confirmed the test fails with `expected 2.0.0 (the
   version just left) to remain reachable for a future roll-forward, got
   history: [{Version:1.0.0 ... IsActive:true}]`, restored the fix,
   confirmed it passes.

**A related, softer finding, fixed as a side effect of a different
approach:** the manual's "(the current one is marked)" wording assumed the
active version routinely appears in the fetched history — but since
`StoreVersion` only ever snapshots the version being *left*, a plugin that
has only ever been freshly installed or straight-updated (never rolled
back) has its active version absent from `versions/` entirely, so
`is_active` would rarely be true in the common case. Rather than teach the
backend to synthesize a fake history entry, the currently-installed
version is now rendered client-side from `p.version` (already present in
the page's own per-card data) as a fixed first line, with the fetched
history filtered to exclude anything matching it — always accurate,
independent of `GetVersionHistory`'s on-disk state, and simpler than
threading an extra query through the handler.

**Two nitpicks, also fixed:**

- `sort.Slice` → `sort.SliceStable` (equal-mtime ties were previously
  nondeterministic; no functional bug found, but no reason not to make it
  deterministic).
- The sort-order test used `1.0.0`/`2.0.0`, where reverse-chronological
  and reverse-alphabetical order happen to agree — it would have passed
  against a sort-by-name-descending bug just as well as the real
  time-based sort. Renamed to `1.0.0` (newer/active) vs. `9.0.0` (older),
  where the two orderings disagree, so the test now actually pins the
  sort key it claims to.

**Confirmed clean, no action needed:** no missing `os.MkdirAll` (the new
handler is read-only), `paths.Plugins()` used correctly (matches
`handleRollbackPlugin`'s own sourcing), `VersionInfo.Path` never reaches
the wire (verified via grep — marshalled in exactly one place — and by a
test asserting the full on-disk path string is absent from the response
body), no XSS surface (`x-text` throughout, no `x-html`), rolling back to
the already-active version is prevented in both the UI (`x-show`) and the
server (`Rollback` errors "already at version"), the manager-auth gate is
correct (this endpoint exposes local device state and is the entry point
to a manager-only mutation, unlike the genuinely-public marketplace-browse
endpoints), replica-till behavior is unaffected (no replica guard needed
on a read; the existing rollback POST's replica guard is untouched),
help-drift baseline counts match the guard's own live output exactly, and
the ar/fa/tr translations are genuine and idiomatic, not copy-pasted
English or machine garbage.

**Explicitly not fixed, and why:** the pre-existing i18n concatenation
pattern for the confirm dialogs (`T + ' ' + name + ' ' + version + '?'`,
which reads awkwardly in Turkish word order and mixes an ASCII `?` into
RTL text) is identical to the existing `confirm_uninstall` pattern
elsewhere on this same page — not a regression this card introduces, and
fixing the whole page's confirm-dialog i18n approach is out of this
card's scope.

## Verified beyond automated tests

- Full repo `go test ./...` (not just the touched packages) — all green.
- `golangci-lint run ./...` (whole repo) — 0 issues.
- Manually inspected the regenerated `en` and `ar` (RTL) screenshots of
  `/plugins`: the new "Versions" button renders cleanly in the action row,
  wraps correctly alongside Export/Uninstall, and the Arabic layout mirrors
  correctly with no overlap — confirms the change is layout-safe at the
  screenshot harness's viewport.
- Did **not** visually verify the *opened* Versions panel with real
  multi-version snapshot data in a live browser — the seed fixtures the
  docs-shots harness uses don't include a plugin with on-disk version
  snapshots, and building one was judged disproportionate to this card's
  scope. The Go handler tests cover the full JSON contract (sort order,
  active-flag correctness, no path leak, empty-array shape); the client
  template logic (filtering the current version out of the fetched list,
  the loading/error states) is straightforward Alpine.js binding using the
  same primitives (`x-show`, `x-text`, `x-for`) already proven elsewhere on
  this exact page — not re-verified pixel-by-pixel in an opened state.
  Flagging this explicitly rather than implying full visual coverage.
- No dedicated Playwright e2e spec exists for `/plugins`' interactive
  actions (enable/disable/uninstall/update also have none — only Go
  handler-level tests) — consistent with this page's existing test-depth
  convention, not a new gap this card introduces.

## Safe to merge

Yes. `merge_method: "merge"` (never squash/rebase — ut-docs#250).

## Explicitly deferred

- The 6 new `en.json` keys need follow-up PRs in
  `ut-plugin-language-{de,es}` — `lang-pack-drift` is advisory on this PR
  (new keys, nothing to compare against yet) and blocking on `main` push;
  owned by whichever lane merges this, in the same cycle, per
  `scrum-master/SKILL.md`'s "work that has no card is not covered" rule.
- Whether `Rollback` should also restore live install-directory *files*
  (it currently only updates DB rows/entries against the target's already-
  extracted `versions/` snapshot) is a pre-existing question, out of this
  card's scope — noted by the independent review, not something this
  change touches or regresses.
