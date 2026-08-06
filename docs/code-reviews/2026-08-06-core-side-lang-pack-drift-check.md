# 2026-08-06 — Core-side language-pack drift check (replacing an idle-prone schedule)

Card: [ut-docs#299](https://github.com/universaltill/ut-docs/issues/299) (p2)
Branch: `feat/299-core-lang-pack-drift-check`

## What shipped

Each external `ut-plugin-language-*` repo (`ut-plugin-language-de`,
`ut-plugin-language-es`, both public) carries its own ratcheting key-drift
guard (`scripts/check-key-drift.sh`, from ut-docs#292/#296), triggered on
that repo's own push/PR plus a weekly `schedule:` cron — the cron is the
*only* mechanism that catches drift originating in **core** (a new
`web/locales/en.json` key lands, nothing in the pack repo ever changes, so
its push trigger can't fire). Problem: GitHub auto-disables a
`schedule:`-triggered workflow in a public repo after 60 days with no
activity in *that* repo — exactly the "pack repo has gone quiet" condition
the cron exists to cover, so its coverage silently reaches zero precisely
when it's needed most.

New, in `universal-till` (an always-active repo — this is the actual fix):

1. `scripts/ci/check-lang-pack-drift.sh` — for each known pack repo (`de`,
   `es`; a third pack is a one-line list edit), fetches that pack's own
   `scripts/check-key-drift.sh` + `locales/<code>.json` +
   `i18n-baseline/<code>.*.txt` over unauthenticated
   `raw.githubusercontent.com` reads, lays them out in the pack's expected
   relative directory structure in a temp dir, and runs the pack's own
   unmodified script against **core's own local `en.json`** via
   `UT_CORE_EN_JSON`. No reimplementation of the drift-check logic — one
   canonical algorithm, exercised from two places (de-duplicating the
   *pack-side* copies into one shared implementation is ut-docs#312,
   deliberately out of scope here).
2. `.github/workflows/lang-pack-drift.yml` — triggered on
   `push: branches: [main]` + `workflow_dispatch`, deliberately **not**
   `pull_request`: a lagging external pack repo must never block an
   unrelated core feature PR from merging. It surfaces as a red run on
   `universal-till`'s own Actions tab within minutes of the merge that
   introduced the drift, instead of up to a week later on a cron that
   might not even still be enabled.

## Verified against live state (not synthetic)

Running the new script against the real, current state of both packs
found genuine, pre-existing drift:

```
check-lang-pack-drift: checking ut-plugin-language-de (locale: de)
check-key-drift: 1 core key(s) missing from locales/de.json and NOT in the baseline (new drift):
  - import.warned
check-lang-pack-drift: ut-plugin-language-de FAILED (see above)

check-lang-pack-drift: checking ut-plugin-language-es (locale: es)
check-key-drift: ok -- 170/1088 core keys translated, ...
check-lang-pack-drift: ut-plugin-language-es ok
```

`import.warned` landed in core's `en.json` via `universal-till@b83d9d8`
(ut-docs#293, 2026-08-05) and was never backfilled to the German pack's
baseline — a live instance of the exact bug class ut-docs#292 fixed,
caught by this new mechanism on its very first real run. That's the
"demonstrated: land a core key a pack lacks, show the mechanism fires"
acceptance criterion, satisfied by an actual regression rather than an
injected one. Filed separately as **ut-docs#315** (pack-content fix, a
different repo, its own review cycle — not a rider on this PR) rather than
fixed here.

Also verified directly: a pack fetch pointed at an unreachable host/repo
hard-fails (never silently exits 0) — same rule the pack scripts
themselves already follow.

## Independent review (Opus, fresh context) — 1 HIGH, 1 MEDIUM, 2 LOW, all fixed

**HIGH, fixed — RCE blast radius with a writable token in the workspace.**
The workflow declared no `permissions:` block, and `actions/checkout@v4`
defaults to `persist-credentials: true`, writing the job's `GITHUB_TOKEN`
into `.git/config`. The job then executes an **unpinned** script fetched
from each pack repo's `main` — a repo this pipeline doesn't control write
access to. A compromised pack-repo commit would run arbitrary code on a
runner holding a push-capable core credential. The reviewer also caught
that the original script comment's justification ("same trust model the
pack's own script uses to fetch `en.json`") was wrong: that fetch parses
JSON as pure data and never executes it — data-ingest and code-execution
are exactly the trust boundary that matters here, and the existing
precedent doesn't cover it. Fixed: `permissions: contents: read` on the
workflow, `persist-credentials: false` on checkout, `timeout-minutes: 10`
on the job (least-privilege — worst case is now a wasted CI run, not a
path back into this repo); the script's own comment corrected to state
the real mitigation instead of the invalid precedent claim.

**MEDIUM, fixed — no retry/timeout on the fetches.** Plain `curl -fsSL`
meant any transient `raw.githubusercontent.com` 5xx or a shared-runner-IP
rate limit would red-X `main` indistinguishably from real drift — and a
guard that cries wolf on network noise gets ignored, landing in the same
"nobody looks" end-state this card exists to prevent. Fixed:
`--retry 3 --retry-all-errors --retry-delay 2 --connect-timeout 10
--max-time 60`. Re-verified live: a request to a genuinely nonexistent
repo now retries (visibly, in the log) before failing, rather than
failing instantly on what could have been a blip.

**LOW, fixed — one pack's fetch failure aborted the whole run.** The
original `fetch()` called `exit 1` directly, so with a bad first pack, the
second pack was never checked at all — inconsistent with the loop's own
`overall_fail` accumulation pattern, which is designed to report every
pack. Fixed: `fetch` now `return`s non-zero, the loop sets `overall_fail=1`
and `continue`s to the next pack. Re-verified live by injecting a bogus
repo ahead of the real two in the `PACKS` list: the bogus entry fails and
is reported, and both `de` and `es` are still checked afterward and
report correctly.

**LOW, fixed — no provenance in the failure log.** The reused pack script
only populates its own `core commit:` line in its network-fetch branch, so
running it via `UT_CORE_EN_JSON` always printed `unknown` — combined with
fetching from an unpinned `main`, a failing run recorded neither which
core commit nor which pack commit produced it, making an unattended
failure weeks from now unreproducible by log alone. Fixed: the outer
script now logs `core commit: <real SHA>` (from `$GITHUB_SHA` in CI, or
`git rev-parse HEAD` locally) up front, and attempts a best-effort,
non-fatal lookup of each pack's current `main` SHA via the unauthenticated
GitHub API (low rate limit on shared runner IPs, so never allowed to gate
the actual check — confirmed by testing in this sandbox, where the
API call is itself blocked/403's, and the script correctly degrades to
logging `unknown` for the pack SHA rather than failing).

**Noted, not a code change — operational sequencing.** Merging this makes
the very next `lang-pack-drift` run on `main` red, because of the live,
pre-existing `import.warned` gap (ut-docs#315). That's the guard correctly
doing its job, not a regression from this PR — flagged in the issue
close-out so it isn't mistaken for one when the run shows red.

**Confirmed correct by the reviewer, no changes needed:** the fetch/exec
model's assumptions about each pack repo's file layout (verified against
the real, live repo contents, not just the local script's comments); the
`UT_CORE_EN_JSON` env var contract, including its caller-cwd-relative-path
handling; every hard-fail path (missing local `en.json`, any fetch
failure, any pack failure) — no route to a silent exit 0 found; the
`push`-only (no `pull_request`) trigger design; YAML validity and
consistency with `ci.yml`'s `actions/checkout@v4` pin; style/structure
consistency with `guard-i18n.sh`/`guard-data-access.sh`; scope (no
pack-script dedup, no `ut-infra/uptime.yml` change, no pack-content
translation — all correctly left to their own separate cards).

## Verification beyond the automated suite

- `bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-i18n.sh`:
  both pass (neither is affected by this diff — CI tooling only, no Go or
  template changes).
- `go build ./...`: clean.
- `go test ./...`: every package passes except one pre-existing, unrelated
  failure — `internal/issuereport`'s `TestSaveCleansUpDirectoryOnWriteFailure`
  (already tracked as ut-docs#258, fails under this sandbox's root-run
  environment; confirmed unrelated — this diff touches no Go source).
- `bash -n` + a real run of `scripts/ci/check-lang-pack-drift.sh`, before
  and after every reviewer fix; `python3 -c "yaml.safe_load(...)"` on the
  workflow file.
- No UI/visible surface touched — CI tooling only, so the tester skill's
  screenshot/driven-run requirement doesn't apply; not skipped, genuinely
  out of scope for this card.

## Safe-to-merge verdict

Yes, after the four reviewer fixes above. The independent review's most
valuable finding was the HIGH: the original script comment's confident but
incorrect trust-boundary precedent ("same model as the data fetch") is
exactly the kind of self-reinforcing reasoning a same-model self-review
would have been most likely to wave through unchallenged.
