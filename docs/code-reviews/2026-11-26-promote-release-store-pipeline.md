# Review: promote-release store pipeline (ut-docs#6407, #6408)

Date: 2026-11-26. Branch `feat/promote-release-store-pipeline`. Paired docs PR: ut-docs ADR-0194 Amendment 2
(`docs/promote-release-adr-0194-amend2`). Owner order 2026-11-26 (ut-docs#6408) opened the Microsoft Store for pipeline
submission; Apple and Google Play production stay closed.

## What shipped

- `promote-release.yml`: dispatch from main with `version`; preflight (main, tag on main, published and latest release,
  `msstore-submit` environment has a required reviewer, admin bypass off, deployments `main` only), then the gated
  `msstore-submit.yml` via `workflow_call`, iOS TestFlight and Play internal status (one re-dispatch on a real run, never
  waits on a Play approval), and one summary table (run summary plus the standing issue "Store release log").
- `msstore-submit.yml`: `workflow_call`, `verify_only`, outputs; package URL is the publish-installer blob;
  `store-release/microsoft.opened` + `store-release:allow ut-docs#6408` marker; `release.yml` dispatches verify_only after
  `publish-installer`.
- No secret, no PAT; OIDC only on the gated submit job; GitHub-hosted runners only.

## Review

Independent review by Fable (code by Sonnet Dev, tests by a separate Tester). Re-verified personally: the two Tester gaps
were shown unpinned (mutating the workflow_call default stayed green because the old helper read the first block only) and
are now RED under mutation.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| T-1 | medium | workflow_call `dry_run`/`commit` defaults unpinned | Fixed: `cdflt` helper reads the workflow_call block; mutation goes RED |
| T-2 / R-3 | medium | Direct dispatch of `msstore-submit.yml` from a branch (or from main) skipped every gate | Fixed: first verify step fails a non-verify_only run off main; `promote-preflight.sh` now also runs inside verify and again after the approval (test pins both) |
| R-1 | medium | Stale approval could submit an older build over a newer one | Fixed: the post-approval preflight re-checks "latest release" and the environment rules |
| R-2 | medium | Post-approval digest check read the GitHub asset, but the Store fetches the blob | Fixed: `msstore-submit.sh` re-downloads and hashes the blob before the PUT; tampered-blob test |
| R-4 | medium | Workflow-level `concurrency: msstore-submit` let a parked approval block or cancel verifies/promotes | Fixed: concurrency moved to the submit job, keyed by tag |
| R-6 | low | Log said "submitted" for a commit=false draft | Fixed: `COMMIT` passed, "package set (draft, not committed)" |
| R-7 | low | Log issue chosen by title only, an outsider could pre-create it | Fixed: `--author app/github-actions` filter |
| R-9/10, T-4 | low | Docs understated promote (re-dispatch), stale marker location, missing `verify_only`, local-path reference, card placeholder | Fixed in the ut-docs PR |
| R-5 | low | First real promote of v0.36.2 with Play enabled would not match the old play-publish run-name and re-dispatch once | Accepted: Play is disabled (`PLAY_PUBLISH_ENABLED` unset); use a release cut after the merge |
| R-8 | low | `actions/checkout@v7`, `setup-go@v7`, `upload-artifact@v7` tag-pinned in msstore-submit.yml (pre-existing) | Deferred: pin by SHA in a follow-up card (consistent with the rest of the repo's first-party tags) |
| T-3 | low | Environment rules are read at run start and again after approval, but an admin could still change them in between | Accepted, documented in the preflight header |

Accepted note: the Fable reviewer could not test locally whether a called workflow's top-level `concurrency` attaches to
the caller; the fix (job-level concurrency) is correct either way.

## Verification beyond unit tests

Ran green: promote-preflight/stores/summary/release-workflow, release-msstore-verify, msstore-submit/verify/workflow,
guard-store-private (+ the guard on the real tree); actionlint clean on promote-release.yml and msstore-submit.yml
(release.yml/ci.yml keep four pre-existing findings); shellcheck clean on changed scripts. Not run: a real workflow run
(the dry_run proof against v0.36.4 runs after merge, from main, which the preflight requires) and no Store call.

Pre-flight: ADR amended and `adr/README.md` index unchanged (status stays Accepted) -> updated in the ut-docs PR;
reference pages (store-distribution, runbook) updated in the ut-docs PR; README n/a (no claim changed); en.json/help
topics n/a (CI only, no shop-visible surface); plan prices n/a; review record present; admin "Release" button deferred ->
ut-docs#7078; Play/App Store opening n/a (stay closed).

Verdict: approve after CI green. Merge as a merge commit; the dry_run run is the post-merge proof.
