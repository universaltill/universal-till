# 2026-08-23 — docs-shots: /locations and /registers never screenshotted (ut-docs#900)

## What shipped

`e2e/tests-docs/docs-shots.spec.ts`'s harness only ever screenshots a help
topic's `routes[0]`. Two topics declare more routes than that:

- `web/help/en/inventory.md`: `routes: [/inventory, /locations, /ui/inventory/stock-table]`
  — `/locations` has never been captured.
- `web/help/en/multitill.md`: `routes: [/tills, /ui/tills/pending-pairings, /registers]`
  — `/registers` has never been captured.

`internal/manual.go` only supports **one** generated screenshot per topic
(`web/help/img/<locale>/<id>.png`, keyed by topic id, injected once after
the topic's `<h1>`) — there is no per-route screenshot slot, so reordering
`routes[0]` would only swap which single page gets captured, not add
coverage. A real multi-screenshot-per-topic fix would mean extending three
files the repo's own comments say must stay in lockstep (`lib.js`,
`write-manifest.js`, `guard-docs-shots.sh`) — judged disproportionate for
a `complexity:easy`, "low priority — tooling/documentation gap, not a
functional defect" card.

Per the issue's own option (b), this change **records the gap instead of
building around it**:

- Extended the doc comment above `routedTopics()` in `e2e/tests-docs/lib.js`
  explaining the one-screenshot-per-topic limitation, naming both affected
  topics/routes, and sketching the two real fixes (split the route into its
  own topic, or extend the harness) for a future card.
- Added a one-line pointer comment (`#`-prefixed, inside the front matter)
  next to `routes:` in both `inventory.md` and `multitill.md`.
- Regenerated `web/help/img/manifest.json` via `make docs-shots` since the
  topic markdown changed (front-matter comment lines count toward the
  topic hash) — no screenshot image content changed, since front matter is
  never rendered.

No functional/behavioral change. No new user-facing string (front-matter
comments are dev/CI-only, never rendered to a shop owner).

## Independent review

Spawned a fresh-context Sonnet subagent (per `complexity:easy` routing —
Dev and Review both at Sonnet, but a clean instance that never saw the Dev
reasoning) with the full diff and explicit instructions to verify the
front-matter parsing safety claim in both parsers, actually run the gate,
and check the claims in the added comments against `internal/manual.go`
itself rather than take them on faith.

**Verdict: SAFE TO MERGE AS-IS.** No findings requiring a fix. Confirmed:

- A `#`-prefixed front-matter line is safely skipped by both parsers —
  Go's `internal/manual.parseTopic` (short-circuits to the comment-skip
  branch before the "bad front-matter line" error path) and JS's
  `routedTopics()` (the anchored key-regex can never match a line starting
  with `#`, so it's silently skipped). `scripts/ci/checkhelptopics` has no
  independent parser — it calls the real `internal/manual.Load()`, so
  there's exactly one Go-side parser to verify.
- Full gate re-run independently and green: `gofmt -l .`, `go build ./...`,
  `go vet ./...`, `go test ./...` (full suite), `guard-help-topics.sh`,
  `guard-docs-shots.sh`, `guard-i18n.sh` (correctly out of scope — it never
  scans `web/help/**`), `guard-compliance-claims.sh` (in scope, scanned,
  nothing forbidden).
- `manifest.json` diff is minimal — exactly the 2 expected `en` topic-hash
  entries changed (`inventory`, `multitill`); `fa`/`ar`/`tr` hashes and
  `surface_sha256` untouched.
- The technical claims in the added comments (one screenshot slot per
  topic, route-ownership exclusivity allowing a future topic split) were
  checked directly against `internal/manual.go`'s `Load()` — accurate.
- No README/locale-file update needed; no secrets, personal names, or real
  shop names in the diff.
- Noted, non-blocking: the translated topic files (`fa`/`ar`/`tr`) weren't
  given the matching `#` pointer comment (routes are canonically declared
  on the English topic per `manual.go`'s own doc comment, and the
  authoritative explanation lives in `lib.js`, not per-locale) — a
  reasonable one-line follow-up if ever wanted, not a merge blocker.

## Verified beyond automated tests

- `make docs-shots` was actually run against a live throwaway till server
  (Playwright, 84/84 screenshot tests passed, all 4 locales) to confirm
  the front-matter change doesn't break capture for `inventory`/`multitill`
  or any other topic, and to regenerate the manifest for real rather than
  hand-editing hashes.
- Confirmed via `git diff` that the actual `/inventory` and `/tills`
  screenshots (routes[0] for their topics) are byte-identical before and
  after — the front-matter comment produces zero visual change, as
  expected (front matter is never rendered).
- Unrelated screenshot drift observed during the `make docs-shots` run
  (`alerts`, `designer`, `sell` PNGs across locales) — attributable to a
  pre-installed Chromium version mismatch (141.0.7390.37 vs. the pinned
  149.0.7827.55; the harness itself warns this is non-fatal) and, for
  `designer` specifically, its already-documented baked-wall-clock
  non-determinism — was **reverted before commit**, since none of it is
  required by `guard-docs-shots.sh` (which hashes topic markdown, not PNG
  bytes) and none of it is caused by this change. Kept the diff scoped to
  exactly the 4 files this task touches.

## Deferred / explicitly out of scope

- Extending the harness to support more than one screenshot per topic —
  not built; noted as a possible future backlog card in `lib.js`'s comment
  if it's ever worth the cost for a specific topic.
- Splitting `/locations`/`/registers` into their own manual topics — same,
  not built, would need real content authored in all 4 locales.
- Adding the matching `#` pointer comment to the `fa`/`ar`/`tr` copies of
  `inventory.md`/`multitill.md` — non-blocking per the review above.

## Safe-to-merge verdict

Yes — independent review found nothing blocking, full gate green (build,
vet, full test suite, all touched guards), diff scoped to exactly what the
issue asked for.
