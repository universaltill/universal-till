# Review: owner-excluded help topics are `public: false` (PR #2498)

Date: 2026-11-12 · Branch: `fix/claim-help-not-public` · Head reviewed: `2229d7a8b`
Author: Sonnet (Dev) · Reviewer: Fable 5.2, independent fresh context (read-only worktree)

## What shipped

- `web/help/{ar,de,en,fa,tr}/{claim,support-partner,translate-mode,translations,fiscal-device,fiscal-register}.md`:
  front-matter `public: true` → `public: false` (30 files, one line each).
- `internal/manual/manual_test.go`: `TestOwnerExcludedHelpTopicsNotPublic`
  (the six ids exist in English and none is `public: true` in any locale).

## Verified

- **Diff scope:** 31 files; the 30 help files change exactly one line each
  (30 × `-public: true`, 30 × `+public: false`), no body text touched.
- **Exclusion list:** equals the "NOT public" block of ut-website
  `data/help-public-allowlist.txt` (owner decision 2026-11-04), all six.
  The 41 topics still `public: true` are the 40 allow-listed ones plus
  `notices` (flagged public in the till, absent from the website list —
  pre-existing, the flag wins, not caused by this PR). Nothing the owner
  wants public was hidden.
- **In-app help unaffected:** `Topic.Public` / `PublicSet` are only parsed
  in `internal/manual/manual.go` and read by tests; no handler, search,
  route map or template filters on them. The six topics keep rendering in
  the till, and `/help/claim` links from `multitill.md` and `users.md`
  still resolve there. `public` means "published outside the till" only.
- **Website (ut-website `src/lib/help.ts`):** `public: false` always wins
  over the allow-list; links from public topics to a non-public one render
  as plain text (`rewriteHref` returns null), so `multitill` and `users`
  do not get dead links. 41 public topics ≥ `MIN_TOPICS` (10).
- **TDD claim:** on an archived scratch copy of HEAD, flipping
  `de/claim.md` back fails the new test ("de/claim.md is public: true,
  but the owner excluded it"); flipping `claim` in all five locales (the
  original bug) fails only the new test, with five errors — the older
  locale-agreement test cannot catch it. Scratch copy removed; worktree
  untouched.
- **Gate:** `go build ./...`, `go vet ./internal/manual/`,
  `go test ./internal/manual/ ./internal/pages/ -run 'Help|Manual'` pass;
  guard-help-topics, -breadcrumbs, -drift, -terms pass.

## Findings

1. **Blocker — docs-shots manifest is stale; main goes red on merge.**
   `scripts/ci/guard-docs-shots.sh` fails on this branch (it passes on the
   merge-base): "topic markdown changed since its screenshot was taken:
   en/claim, en/fiscal-device, en/fiscal-register, en/translations". The
   per-topic hash in `web/help/img/manifest.json` is a whole-file sha256,
   so the front-matter line counts. The PR body's "surface hash unchanged"
   is true but is not the hash that moved. `run-docs-shots-guard.sh` only
   warns on `pull_request` and hard-fails on push to main, so the PR looks
   green and main breaks right after the merge. Fix on this branch: run
   `make docs-shots` (last and alone) and commit the manifest; no PNG
   should change. **Open.**
2. **Major (cross-repo, must land with this) — ut-cloud's help index
   builder rejects the new state.** ut-cloud
   `internal/helpdocs/allowlist.txt` still lists all six ids, and
   `helpdocs.Build` fails closed on an allow-listed page marked
   `public: false`. Run against this branch:
   `go run ./cmd/helpindex -till <branch> -check` →
   `helpindex: ar/claim.md is marked public: false but is on the allow-list`.
   Effect after merge: `refresh-help-index.yml` (4-hourly) fails on every
   run, so the assistant connector keeps serving the six topics from the
   stale embedded index and stops picking up any other help change.
   ut-cloud PR CI is not blocked (main drift is a warning there). So this
   PR alone does not take the topics out of the connector, which the test
   comment and PR body imply. Needs a ut-cloud PR: drop the six ids from
   `allowlist.txt` and regenerate `internal/helpdocs/data/index.json.gz`.
   **Open — no card filed by the reviewer; the orchestrator files it.**
3. **Minor — the test hard-codes the six ids;** the source of truth is a
   comment block in another repo. Acceptable (the list is an owner
   decision and small); a seventh exclusion must be added in both places.
   Accepted.
4. **Note — `notices` is `public: true` but is not in the website
   allow-list** (pre-existing). Published because the flag wins; worth
   adding to the list for clarity. Not in scope.

Pre-flight: `web/help/` topics — updated (the change itself); `make docs-shots`
/ `web/help/img/manifest.json` — NOT updated, finding 1; `reference/` page —
n/a (no behaviour change in the till); `README.md` — n/a; `en.json` /
`en.meta.json` — n/a (no key); ut-website — n/a (reads the flag, allow-list
already excludes the six); ut-cloud help index allow-list — NOT updated,
finding 2, card to be filed; ADR — n/a; review record — this file.

## Verdict

**Changes requested.** The 30-line change and the test are correct and
do what the owner asked for the website. Do not merge until finding 1 is
fixed on this branch; finding 2 needs its ut-cloud PR ready to merge
alongside.
