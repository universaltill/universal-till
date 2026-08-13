# Code review: narrow guard-docs-shots.sh's internal/pages/**.go surface

**Date:** 2026-08-13
**Author (Dev):** scrum-master pipeline, Sonnet (complexity:medium)
**Reviewer:** independent Opus subagent (isolated worktree)
**Card:** universaltill/ut-docs#620

## What shipped

`scripts/ci/guard-docs-shots.sh` (and its generation-side mirror,
`e2e/tests-docs/lib.js`) previously treated **every** non-test `.go` file
under `internal/pages/` as part of the manual-screenshot "surface" — any
change there, however unrelated to rendering, forced a `make docs-shots`
re-run or the guard failed. This was deliberately coarse per the guard's
own original header comment ("can't tell whether a given Go change
affects rendered output"), but concretely burned landing ut-docs#535: a
purely backend fix inside `import_page.go`'s `mergeTakeawayOverrides` (a
different top-level function from the one registering `/import` and
calling `httpx.Render`) failed the guard for zero pixel reason, and got
worked around by hand-patching `manifest.json`'s hash field instead of
legitimately regenerating screenshots.

Product owner's decision on #620 (2026-08-13): narrow the guard, erring
toward false positives over false negatives.

**New rule:** a `.go` file under `internal/pages/` is excluded from the
surface hash only when it registers at least one route (via
`mux.HandleFunc(...)`/`mux.Handle(...)`, extracted via a regex that
handles both this codebase's `"GET /path"` and plain `"/path"`
registration styles) **and none of those routes is any help topic's
`routes[0]`** — the one URL `docs-shots.spec.ts` actually visits and
screenshots for that topic. A file with **zero** route registrations is
kept (unchanged from before) — see "Review" below for why. A file that
registers even one screenshotted route is still hashed as a whole file,
not split by function.

`web/help/img/manifest.json` regenerated via `make docs-shots` (now
runnable in a cloud session per the already-merged ut-docs#622) — only
`algorithm` and `surface_sha256` changed; `topics` hashes are
byte-identical (they hash topic markdown, not PNGs). The known
non-deterministic `alerts`/`designer` PNG diffs (wall-clock-baked receipt
preview timestamp, documented in `docs-shots.spec.ts`'s own comment) were
deliberately not committed, matching #622's precedent.

New `scripts/ci/guard-docs-shots_test.sh` (follows the existing
`guard-*_test.sh` plant/expect_pass/expect_fail convention) and a new CI
step wired up for it — the four sibling guards each had a paired
regression-test CI step; this guard had none, a real gap found during
testing.

## Review — round 1 finding, fixed

Independent review (Opus, isolated worktree) found the first draft's
design premise false for one real class of file: **route-less shared
helpers still move screenshotted pixels.** The first draft excluded any
file with zero route registrations, reasoning "no route, no way to
affect a screenshot." The reviewer disproved this concretely: adding an
entry to `internal/pages/init.go`'s `baseMenu` (rendered as a nav tile in
`web/help/en/menu.png` via `menu_page.go` → `web/ui/pages/menu.html`'s
`{{ range .Tiles }}`) went undetected by the first draft's guard, while
`main`'s original (coarse) guard correctly caught it — a genuine
regression, not a pre-existing gap. Also flagged: `internal/pages/
common/state.go` (`BuildMenu`), `internal/pages/common/deps.go`
(`MenuSnapshot`), and `internal/pages/themes.go` (`availableThemes`, feeds
`/settings`' theme picker) — all route-less files that feed a
screenshotted page's template data without registering a route
themselves.

**Fixed:** flipped the rule from "include only if it registers a
screenshotted route" to "exclude only if it registers routes and none of
them is screenshotted." A file with zero route registrations is now kept
unconditionally (matching the original coarse behavior for that specific
case), while a file like `import_page.go` — which positively registers
`/import`, a real but unscreenshotted route — is still excluded. This
still fully fixes the ut-docs#535 case (re-verified below) while closing
the regression the reviewer found. Re-verified by planting the reviewer's
own repro (a new `baseMenu`-style entry in `init.go`) directly against
the fixed guard: now correctly fails.

Both header comments (guard script and `lib.js`) rewritten to state the
actual rule and explicitly document why "zero registrations → excluded"
was rejected, so a future reader doesn't reintroduce the same mistake.

### Other findings

- **Missing review record** (this file) — the header comments referenced
  "see the code review" before one existed. Fixed by writing this record.
- **LOW, accepted:** the route-extraction regex is anchored to the `mux.`
  receiver name. Every real non-test registration in this package uses
  that name today (240/240, confirmed by the reviewer via independent
  extraction across all 75 non-test files). A future file using a
  differently-named `*http.ServeMux` variable would register zero routes
  under this regex and so falls into the "zero registrations → kept"
  bucket — an accepted residual gap, and safe by construction (it can
  only ever cause over-inclusion, never a new false negative). Documented
  explicitly in both header comments rather than left implicit.
- **NIT, fixed:** the test harness's `expect_fail` accepted any guard
  failure as a pass, not specifically the surface-staleness message.
  Tightened to assert the actual "the app surface" message so a test
  can't false-pass on an unrelated guard failure (e.g. a missing
  manifest).

## Verified beyond automated tests

- **TDD independently re-verified twice** (once by the implementing Dev
  pass, once again by the reviewer): reverted `guard-docs-shots.sh` +
  `lib.js` + `manifest.json` to `main`, reran
  `guard-docs-shots_test.sh`, confirmed it fails with exactly the claimed
  false-positive errors while the two must-still-catch cases stay green;
  restored the fix, confirmed all green again.
- **The original bug reproduced and confirmed fixed, on the real file**:
  a one-line no-op edit inside `internal/pages/import_page.go`'s
  `mergeTakeawayOverrides` (not a synthetic fixture) — guard passes on
  this branch, fails on `main`.
- **The reviewer's regression case reproduced and confirmed fixed, on
  the real file**: adding a `baseMenu` entry to `internal/pages/init.go`
  (zero route registrations) — guard now correctly fails (would have
  silently passed under the first draft).
- **Python/JS lockstep confirmed empirically** (not eyeballed) by the
  reviewer: identical route extraction across all 75 non-test files/237
  literals, identical 17 topic→`routes[0]` pairs, identical resulting
  surface hash between the two implementations and the manifest.
- **`manifest.json` diff scope confirmed**: only `algorithm` +
  `surface_sha256` changed; `topics` dict byte-identical; no PNGs, no
  secrets, no real client/shop name in the diff.
- Full gate green after the fix: `go build ./...`, `go vet ./...`, full
  `go test ./...` (whole module), `guard-data-access.sh`, `guard-i18n.sh`,
  `guard-docs-shots.sh`, `guard-help-topics.sh`, plus the other 4 sibling
  guards and their `_test.sh` suites, plus `resolve-chromium_test.sh`
  (#622's suite, unaffected) — all pass.
- `bash scripts/ci/guard-docs-shots_test.sh` — 5/5 cases pass, including
  the flipped "route-less file" case.

## Deferred / out of scope

- The Playwright/Chromium toolchain itself — already fixed by ut-docs#622
  (merged).
- The `chromium-headless-shell` vs. full-Chrome variant gap — already
  filed as ut-docs#632.
- Full call-graph reachability analysis (tracking exactly which
  screenshotted page a given route-less helper feeds, rather than
  conservatively keeping all route-less files) was considered and
  rejected as disproportionate engineering for a CI guard — the current
  design's "err toward inclusion" trade-off (keep all route-less files;
  only exclude a file that positively identifies as a different,
  unscreenshotted page) is simpler, safe by construction, and still
  removes the exact false-positive class this card was filed over.

## Safe-to-merge verdict

Yes, after the round-1 fix. No blockers remaining; the regression the
independent review found is fixed and re-verified against the real repro
that exposed it, not just against a synthetic fixture.
