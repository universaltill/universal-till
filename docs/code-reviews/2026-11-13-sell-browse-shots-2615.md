# Review: help shows each sell-screen browsing layout (ut-docs#2615)

Date: 2026-11-13 · Lane: `lane:cloud-41` · Branch: `docs/2615-sell-browse-shots`
Author (Dev): Sonnet subagent · Reviewer: Opus subagent (different model) ·
Tester and UX: independent subagents.

## What shipped

- **Extra shots per help topic.** An inline image in the English topic,
  `![alt](img/<id>-<part>.png)`, declares an extra docs-shots screenshot for
  that topic. Three readers derive the shot list from that one source, all
  with the same name rule (`[a-z0-9-]`, own topic prefix, `.png`, never
  `<id>.png`):
  - `e2e/tests-docs/lib.js` (`extraShotsByTopic`);
  - `scripts/ci/guard-docs-shots.sh` (`extra_shots`, plus a
    `GUARD_DOCS_SHOTS_PRINT_SHOTS_ONLY` mode the cross-check test compares
    against lib.js);
  - `internal/manual` (goldmark AST).
- **Harness.** `docs-shots.spec.ts` has an `extraShots` recipe table next to
  `dialogShots`. Each recipe has a route, setup, `ready` and teardown. A shot
  declared in markdown with no recipe, or a recipe with no matching
  reference, throws at load. The sell recipes set `sale.browsing_mode`
  through `POST /api/settings/browsing-mode`, capture `/`, assert the layout,
  and restore `category_tabs` in a `finally`. The main `sell` shot now sets
  `category_tabs` first, so it no longer depends on test order.
- **Manifest and guard.** `manifest.json` gets a new `shots` key; a manifest
  without it still loads. `guard-docs-shots.sh` fails on any of these:
  - a missing English PNG;
  - a manifest shot that no topic references (orphan);
  - a referenced shot that was never captured;
  - a malformed reference;
  - an extra shot on a route-less topic.
- **Manual.** Inline images resolve to `/help/img/<locale>/<file>`, fall back
  to `en`, and get `loading="lazy"`. An unresolved image is dropped and
  listed in `Library.MissingImages()`; `checkhelptopics`
  (`guard-help-topics.sh`) fails on that list. `plainText` now strips images
  anywhere in a line, so search snippets stay clean.
- **Content.** `web/help/en/sell.md` has one indented image line inside each
  of the three "Choosing how items are browsed" bullets. 12 new PNGs:
  `sell-browse-{tiles,filters,strip}.png` for en, fa, ar and tr.
  `surface_sha256` is unchanged, and every existing PNG, `sell.png`
  included, is byte-identical. Translated topics pick up their own shots
  once Weblate adds the image lines (ADR-0160).

## Findings (Opus review)

| # | Sev | Finding | Outcome |
|---|---|---|---|
| F1 | minor | A malformed image reference in any locale failed `Load`, so `Builtin()` returned nil and every "?" link was lost. | **Fixed.** In a non-English topic a bad reference is now dropped and reported through `MissingImages()`, which the guard fails on. An English bad reference still fails hard, because that file is the declaration source. Test `TestInlineShot_MalformedRefInTranslationIsDroppedAndReported` was red first. |
| F2 | minor | A shot name can collide with another topic's id. | Deferred → ut-docs#6190 (no collision exists today). |
| F3 | minor | A failed `ready` left the wrong-layout PNG on disk. | **Fixed.** The extras loop deletes that PNG and rethrows. A teardown failure no longer hides the original error (F9, part 1). |
| F4 | minor | Reference-style images and nested-bracket alt text are resolved by Go but never seen by the regex scanners. | Deferred → ut-docs#6190 (none in use). |
| F5 | minor | Orphan `<topic>-*.png` files for a removed shot are not flagged. | Deferred → ut-docs#6190. |
| F6 | nit | `sell-browse-tiles.png` looks the same as the `sell.png` header. | Accepted for now; noted on ut-docs#6190. |
| F7 | nit | The ar, fa and tr extra PNGs are embedded before their topics use them. | Accepted: they are ready for when Weblate adds the image lines. |
| F8 | nit | The test case (a) regex is loose. | Accepted: the Tester confirmed it is not a false pass. |
| F9b | nit | An explicit `"shots": null` in the manifest gives a Python traceback. | Accepted: the manifest is generated, never hand-written. |

Checked and clean: no path traversal (`[a-z0-9-]` names only), alt text is
escaped by goldmark, no external URLs, `MkdirAll` is present, the Python
heredocs are quoted, the guard tests run in the sandbox
(`guard-ci-tests-sandboxed.sh`), and the shared till runs with workers: 1.

## Verification beyond unit tests

- **Tester (PASS):**
  - Every test written after its implementation was broken on purpose in a
    temporary copy, and each one failed. That covers checkhelptopics,
    `internal/pages`, 8 breaks in `internal/manual`, and the 6 new guard
    cases.
  - A second `make docs-shots` run left all 145 files in `web/help/img`
    byte-identical.
  - The teardown was proven directly: with it disabled, the next shot fails
    with "no category tiles".
  - All 12 PNGs were looked at. ar and fa are mirrored, and strip mode has
    no All tab and shows real quick buttons.
  - `go test ./internal/plugins/...` passed in 657s, which clears the Dev's
    600s timeout.
- **UX gate (PASS, no must-fix):** checked with Playwright at 1024x600,
    390x844 and 360x740, plus RTL.
  - The images sit inside their bullets with no horizontal scroll.
  - Alt text is sensible.
  - Lazy-loaded images never stay blank.
  - The status bar is visible at every size.
  - Its suggestions went to ut-docs#6190.
- **Reviewer re-check:** with the fix reverted in a scratch worktree, the
  checkhelptopics test fails; it passes again once restored.
- **Gate:**
  - gofmt is clean, `go build ./...` passes, and golangci-lint reports 0
    issues.
  - shellcheck shows one SC1091 info on an existing `source` line.
  - The ci.yml `build` guards pass, except two that fail for environment
    reasons: the sandbox's shellcheck version, and catimport tags that are
    not fetched here.

Pre-flight: web/help topic updated (`en/sell.md`) + `make docs-shots` run (12 new PNGs + manifest `shots`; existing shots byte-identical); `reference/` page n/a (ut-docs `reference/list-and-dialog-pattern.md` checklist line deferred → ut-docs#6190, ut-docs CI stalled #6033); README n/a (no README text covers help screenshots); en.json/meta n/a (no new key; alt text lives in the help topic, translated through Weblate); ADR n/a (extends the docs-shots harness like `dialogShots`, ut-docs#5581); plugin/manifest mirrors n/a; review record: this file; follow-ups → ut-docs#6190.

## Verdict

Approve. Merge once CI is green.

## CI follow-up (desktop-shell deadcode)

`guard-deadcode-baseline.sh` flagged `Library.MissingImages` (only called by
`checkhelptopics`) and `parseTopic` (after F1, only tests called it). Adding
baseline entries is forbidden in a feature PR (ut-docs#3406), so: `parseTopic`
now takes the `lenient` flag itself (no wrapper), and `Builtin()` logs each
missing inline image once at load. That is a real production caller, and it
makes a shipped-but-missing picture visible in the till's log rather than
silent. `internal/manual` and `checkhelptopics` tests pass, golangci-lint
reports 0 issues, and the baseline-growth guard is green. CI re-runs
deadcode itself, because the local toolchain can't build v0.48.0.
