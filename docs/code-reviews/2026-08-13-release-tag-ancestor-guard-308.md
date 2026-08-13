# Code review: release-tag ancestor guard

**Date:** 2026-08-13
**Card:** ut-docs#308
**Scope:** `.github/workflows/release.yml`

## What shipped

A CI guard in the `Release` workflow's `prepare` job: before a release tag
is built (tag-push trigger) or created (`workflow_dispatch`), verify the
commit is actually an ancestor of `origin/main`. Factored into one
`require_ancestor()` helper called from both entry points.

## Why this card looked bigger than it was

ut-docs#308 was originally filed 2026-08-05 as "every release tag is
unreachable from `main` — the history was rewritten." This cycle picked
the card up after a 2026-08-12 product-owner decision ("main is
authoritative, start a clean v0.3.0, add the ancestor-check CI guard").

Investigating before building turned up that most of the card's
acceptance criteria had already been satisfied by ordinary release
cadence — `v0.2.73` through the current `v0.3.4` were all already real
ancestors of `main`, #293/#300 were already shipped, and the cadence-count
formula already reads correctly off the current tag. That narrowed the
remaining work to one thing: the CI guard itself, which didn't exist yet.
Complexity was downgraded `hard` → `medium` accordingly (see the ut-docs#308
comment thread) — Sonnet built inline, Opus reviewed.

## Independent review (Opus subagent, fresh context)

Given the diff and told to be adversarial, not to rubber-stamp, and to
verify the surrounding narrative's factual claims against the real repo
rather than take them on trust. It did — and the most important finding
wasn't about the code:

### Blocker-class finding — the "history rewrite" diagnosis doesn't hold up

The reviewer ran the same `git merge-base --is-ancestor` checks this
cycle's own investigation had run, on the same working tree, and got a
**different answer**: after `git fetch --unshallow`, all 82 `v*` tags —
**including `v0.2.58`/`v0.2.59`/`v0.2.60`**, the specific tags both the
original 2026-08-05 finding and this cycle's investigation had confirmed
as broken — are genuine ancestors of `origin/main`.

Independently re-verified after the review came back, on the same
checkout, before writing this up:

```
$ git rev-parse --is-shallow-repository
false
$ for t in $(git tag -l 'v*' | sort -V); do
    git merge-base --is-ancestor "$t" origin/main || echo "NOT ANCESTOR: $t"
  done
# (no output — every tag passes)
$ git describe --tags --contains v0.2.58^{commit}
v0.2.58^0
$ git describe --tags --contains v0.2.60^{commit}
v0.2.60^0
```

**Conclusion: there was no history rewrite.** The clone this cycle used
for its initial investigation was shallow (a default/partial checkout,
not explicitly unshallowed before running ancestry checks), and `git
merge-base --is-ancestor` against a shallow clone reports a real ancestor
as "not found" purely because history doesn't reach back far enough —
indistinguishable from a genuine broken lineage unless you know to check
`--is-shallow-repository` first. That almost certainly also explains the
*original* 2026-08-05 finding that opened this card, and very likely
explains why the ancestry check appeared to "self-heal" at `v0.2.73` in
this cycle's first pass (a boundary that only existed because progressively
older tags fell outside a shallow clone's fetched depth, not because
anything actually changed on the remote).

This directly undermines the "abandon v0.2.58-60, they're the old
lineage" framing in the 2026-08-12 decision comment and this cycle's own
first comment on the issue — both were built on the same misread. Filed a
correction on the issue (see close-out) rather than let the record stand
uncorrected, and opened a new backlog card for the actual defect: pipeline
sessions doing git-history forensics on a possibly-shallow clone, with no
standing habit of checking `--is-shallow-repository` first.

**The CI guard itself is unaffected by this** — it's good defense-in-depth
regardless of whether the specific 2026-08-05 incident was real, and the
review's other findings (below) made it correct on its own terms. But its
code comments originally asserted the "2026-08 history rewrite" as
settled fact; rewritten to describe what the guard enforces instead of a
specific unverified incident.

### Should-fix — fixed

1. **Comments overstated an unverified incident as fact.** Rewritten (see
   above) to describe the guard's purpose without asserting the rewrite
   happened as originally described.
2. **`git merge-base --is-ancestor` exit-code handling was too coarse.**
   `1` (not an ancestor) and `128`+ (real errors — missing ref, missing
   `origin/main`, etc.) were both caught by a bare `if ! cmd; then`,
   producing a confidently-wrong "not an ancestor" message on what could
   be an infrastructure problem (e.g. someone later trims `fetch-depth: 0`
   off the checkout step). Fixed: `require_ancestor()` now captures the
   real exit code and fails closed with a distinct message for anything
   other than `0`/`1`, plus an explicit `--is-shallow-repository` guard
   ahead of the check (see the blocker finding above — this is the same
   failure mode, now guarded against in the workflow itself, not just
   avoided by accident in this investigation).
3. **Push-path: a rejected tag stays on the remote and poisons the next
   auto-bump.** By the time `prepare` runs on `push: tags`, the tag
   already exists remotely; the guard can refuse to build a release from
   it, but can't un-push it, and the dispatch path's own `sort -V | tail
   -1` bump logic would silently compute the next version from it. Fixed:
   the push-path call now passes an explicit hint telling the operator to
   delete the tag and retag from `main`.
4. **Refname ambiguity.** `git merge-base --is-ancestor "${TAG}" ...`
   (bare tag name) is now `refs/tags/${TAG}` (matches the qualified form
   already used ten lines below in the pre-existing tag-exists check).
5. **Duplication.** Two near-identical guard blocks factored into one
   `require_ancestor()` helper, called from both entry points — became
   clearly worth it once item 2 added real logic to duplicate.

### Nits — accepted as-is, noted for awareness

- A push-path race exists if a commit and its tag land as two separate
  pushes with the tag first: `prepare` could fetch an `origin/main` that
  doesn't yet contain the commit and fail a legitimate release. Recovery
  is a workflow re-run. Not fixed — rare, non-destructive, and a targeted
  fix (retry/re-fetch) adds complexity disproportionate to a race that
  self-resolves.
- `scripts/release.sh` (the documented manual-tag path) still has no
  ancestry check of its own — it tags whatever `HEAD` is with no
  verification it's on `main`. The CI guard catches the bad tag once
  pushed either way, so this isn't a hole in the acceptance criterion,
  but a local check would give the operator a faster, pre-push signal.
  Left as a follow-up rather than folded into this card — out of scope
  for "add a CI guard."

## Verification

- `python3 -c "import yaml; yaml.safe_load(...)"` — YAML parses.
- `bash -n` on the extracted `run:` script — shell syntax valid.
- `require_ancestor()` extracted and exercised directly against the real
  repo (not a toy fixture) for all four cases: a real ancestor tag (pass),
  `HEAD`/`origin/main` (pass), a synthetic orphan commit built with
  `git commit-tree` off an empty tree — never checked out, no working-tree
  risk — (correctly rejected), and a nonexistent tag ref (correctly caught
  as an infra error, not silently treated as "not an ancestor").
- `go build ./...` — unaffected (no Go files in this diff), ran anyway per
  the repo's "before committing" rule; clean.
- `bash scripts/ci/guard-data-access.sh` / `guard-kiosk-engine.sh` /
  `guard-plugin-menu-read.sh` — clean (no app code touched).
- Not run: `go test ./...` (no Go code changed) and a live workflow
  dispatch (this change can only be fully exercised by GitHub Actions
  itself — the next real release run is the live confirmation; DevOps
  step will watch `main`'s CI post-merge for the guard executing cleanly
  on ordinary, legitimate releases).

## Not in scope for this card

- Adding the same ancestry check to `scripts/release.sh` (noted as a nit
  above).
- Investigating *why* the original 2026-08-05 shallow-clone read happened
  or auditing other pipeline steps for the same footgun — tracked as a
  new backlog card instead of scope-creeping this one.
