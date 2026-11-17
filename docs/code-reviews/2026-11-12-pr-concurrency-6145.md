# 2026-11-12 — cancel superseded PR runs (ut-docs#6145)

**What changed.** Nine pull_request-triggered workflows (`android-ci`, `ci`,
`commit-attribution`, `desktop-darwin`, `docs-shots-determinism`, `e2e`,
`ios-ci`, `lang-pack-drift`, `locale-render-audit`) gain a top-level
`concurrency:` block: group
`${{ github.workflow }}-${{ github.event.pull_request.number || github.ref }}`,
`cancel-in-progress: ${{ github.event_name == 'pull_request' }}`. New
`scripts/ci/guard-pr-concurrency.sh` (+ `_test.sh`, mirrored from ut-cloud#992)
fails any pull_request-triggered workflow lacking one; wired as two steps in
the existing guard job of `ci.yml`.

**Safety.** Only `pull_request` events cancel; pushes to main and
`workflow_dispatch` never do. Workflows that already had a group
(`catimport-tag`, `docs-shots-locale-drift`, `ios-testflight`, `nightly`,
`release-orphan-tag-sweep`) and non-PR workflows (`release`, retry, fixed-date)
are untouched. No job or workflow `name:` changed, so required-check names are
unchanged. Docs-shots surface (web/templates) untouched, no trailer needed.

**Verified.** Guard and its test pass locally on the tree. Not verified: the
cancel behaviour itself, observable only on this PR's second push. Review by
the dev agent only (config-only change).
