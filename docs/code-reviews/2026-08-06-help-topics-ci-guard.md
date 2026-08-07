# Code review: help-topics CI guard (ut-docs#361)

**Change**: `scripts/ci/guard-help-topics.sh` + `scripts/ci/checkhelptopics`
(new Go binary), wired into `.github/workflows/ci.yml`'s `build` job, plus
a `CLAUDE.md` correction.

**Reviewer**: independent fresh-context Opus subagent (no prior conversation
history), per this repo's complexity:medium routing.

## Why

A duplicate/conflicting `routes:` entry across two `web/help/<locale>/*.md`
topics (or any malformed topic front matter) doesn't fail loudly at
runtime — `internal/manual/builtin.go`'s `Builtin()` (the loader every "?"
help link resolves through) swallows the resulting `manual.Load` error into
a `log.Printf` and silently degrades every contextual help link app-wide to
the generic `/help` index. `universal-till/CLAUDE.md` claimed a
`scripts/ci/guard-help-topics.sh` CI guard already prevented this from
shipping; it didn't exist.

## What changed

- `scripts/ci/checkhelptopics/main.go`: calls the real
  `manual.Load(uiassets.HelpFS, "help")` on the embedded help content (same
  call `Builtin()` makes) and fails with `Load`'s own error text — which
  already names the conflicting topics/route or the malformed file — on any
  error. Also checks every locale in `web/locales/*.json` (excluding `en`)
  against `Library.MissingTranslations`.
- `scripts/ci/guard-help-topics.sh`: thin wrapper, `go run
  ./scripts/ci/checkhelptopics`, matching the sibling guards' `ROOT_DIR`/
  `set -euo pipefail` idiom.
- `.github/workflows/ci.yml`: new "Help-topic registry guard" step in the
  `build` job, with the same `GOMODCACHE`/`GOCACHE` env as Build/Test so its
  compilation is cache-shared, not duplicated.
- `CLAUDE.md`: corrected to describe what the guard actually enforces
  (route conflicts, parse errors, locale completeness) rather than
  overclaiming full page-route coverage.

**Design choice**: calls the real `internal/manual` package via `go run`
rather than reimplementing front-matter/route-conflict parsing in
bash/python the way `guard-i18n.sh` reimplements JSON key-diffing. The
route-conflict rule (checked against one map shared across ALL locales, not
per-locale) is subtle enough that a shell reimplementation risked silently
drifting from the real algorithm — the exact bug class this card exists to
close.

## Independent review — three blockers, all fixed

1. **False claim of uniqueness.** The guard script's header claimed it was
   the only thing stopping bad content from shipping. False:
   `internal/pages/help_page.go`'s `Library()` loader already propagates
   the same error, and three existing tests (`TestEveryTopicResolves`,
   `TestManualIsTranslatedInEveryShippedLocale`,
   `TestRouteRegistryResolvesKnownPages`) already fail on it via
   `go test ./...`, which runs in the same CI job. Reviewer independently
   reproduced this by injecting a duplicate route and running the suite.
   Fixed: header comment now states the guard's real, more modest value —
   one dedicated, clearly-named, earlier-failing step instead of several
   confusingly-worded incidental test failures.
2. **`CLAUDE.md` still false.** Its claim that
   `scripts/ci/guard-help-topics.sh` enforces "a new page needs a manual
   topic declaring its `routes:` and a `?` link" remained false — the guard
   never checks page-route coverage (that every registered app route has a
   claiming topic), only the manual's own internal consistency. Fixed:
   reworded to state precisely what's enforced today, and opened
   ut-docs#365 to track the page-route-coverage gap as real follow-up work
   rather than silently dropping it or ballooning this card's scope to
   cover it.
3. **Locale-drift check regressed the test it duplicates.** The original
   implementation iterated `lib.Locales()`, which only lists locales that
   already have ≥1 loaded topic file — so deleting an entire
   `web/help/<locale>/` directory was invisible to the guard (exit 0)
   while the existing `TestManualIsTranslatedInEveryShippedLocale` still
   correctly failed on it. Reviewer proved this with a real deletion.
   Fixed: the locale set now comes from `web/locales/*.json` (the same
   shipped-locale registry `guard-i18n.sh` checks parity against), not from
   what the manual happened to load — `MissingTranslations` correctly
   reports every topic as missing for a locale with zero files, so this now
   catches the case the original implementation missed. Re-verified by hand
   (deleted `web/help/ar/*.md`, confirmed `exit 1` naming every topic;
   restored, confirmed clean `exit 0`).

## Non-blockers addressed

- Missing `GOMODCACHE`/`GOCACHE` env on the new step (the only
  `go`-invoking guard) meant its compilation wasn't cache-shared with
  Build/Test — added, matching their env block.
- Success-message format didn't match sibling guards' `✓ <name> guard: ...`
  convention — fixed.

## Non-blockers logged as follow-up, not fixed here (scope discipline)

- `guard-docs-shots.sh` currently fails on this branch (stale screenshots
  for `bug-reporting`/`multitill`) — confirmed pre-existing, none of its
  hashed fileset is in this diff, and it's the same condition already
  tracked as ut-docs#364 (`blocked:env`, main is CI-red on this guard,
  blocking every open PR's build job). Not this card's problem to fix.
- `manual.go`'s conflict error can name a non-`en` translation as a route's
  "owner" instead of the canonical `en` topic, when `fs.ReadDir`'s
  alphabetical order puts a translation first. Pre-existing, unrelated to
  this diff, but this guard is now that message's primary consumer —
  logged as ut-docs#366.
- `go run`'s bare `exit status 1` stderr line after the real error message
  (cosmetic); a topic that exists only in a non-`en` locale is invisible to
  the check (`lib.ids` is `en`-only, matches existing `manual.go` behavior
  elsewhere) — both noted, neither worth a card on their own.

## Verification

- `go build ./...`, `go vet ./...` — clean.
- `gofmt -l` on the new package — clean (4 pre-existing drifted files
  elsewhere in the repo, unrelated to this diff, tracked as #318).
- `bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-i18n.sh` —
  clean.
- Full `go test ./...` (once, after all fixes) — one failure,
  `internal/issuereport`'s `TestSaveCleansUpDirectoryOnWriteFailure`,
  confirmed pre-existing/environment-only (root-run sandbox, tracked as
  #258), reproduces identically on unmodified `main`.
- Black-box proof re-run after every fix, by both Dev/Tester and the
  reviewer independently: duplicate-route injection (three different route
  pairs across the two passes), malformed front-matter injection, and
  whole-locale-directory deletion — each caught with a clear, correctly
  attributed message; each reverted to a byte-clean working tree
  (`git status --short web/help/` empty) before moving on.
- Guard confirmed to run correctly from a non-repo-root cwd (matches how
  CI actually invokes `bash scripts/ci/guard-help-topics.sh`).
- No UI/runtime surface touched (`internal/manual/`, `internal/pages/`,
  `web/locales/` all confirmed untouched by `git diff --stat`) — no visual
  check applicable; no i18n keys added; no ADR needed (CI hygiene on an
  existing, already-documented mechanism).
