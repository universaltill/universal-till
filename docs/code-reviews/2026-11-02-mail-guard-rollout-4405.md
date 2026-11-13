# Code review — mail-address guard rollout to ut-website, ut-my-shop, ut-admin-app (ut-docs#4405)

**Date:** 2026-11-02 · **Lane:** lane:cloud-41 · **Author:** AI agent (lane) · **Reviewer:** independent AI reviewer of a different model (fresh subagent)

## Change

- `guard-mail-addresses.sh` and `guard-mail-addresses_test.sh` are copied byte-identically into:
  - `ut-website/scripts/`
  - `ut-my-shop/scripts/ci/`
  - `ut-admin-app/scripts/ci/`
- Each of those repos runs both in CI with no path filter:
  - ut-website: a new `check-mail-addresses` job on `ubuntu-latest`. The repo is public, so no self-hosted runner.
  - ut-my-shop and ut-admin-app: a step in the `check` job on `homelab`. Both repos are private.
- The guard header now lists all 7 copies. The failure text and the self-test header say "every copy" instead of "four". The four existing copies (ut-docs, universal-till, ut-cloud, ut-infra) were updated to stay byte-identical. These are comment and message-text changes only; no logic changed.
- `reference/mail-addresses.md` lists the new copies. The plugin repos moved to ut-docs#5250, split out because a cloud lane cannot enumerate them.

## Verification

- `cmp` passes for all 14 files against the ut-docs canonical copy.
- The guard and self-test are green in all 7 repos (website 24 addresses, my-shop 5, admin-app 3, all allowed). No existing address needed fixing.
- A made-up address planted in a throwaway local clone of each new repo makes the guard exit 1.
- `shellcheck` 0.11.0 is clean. universal-till `guard-ci-tests-sandboxed.sh` passes. The new workflow YAML parses.

## Findings

| # | Sev | Finding | Resolution |
|---|---|---|---|
| 1 | low | The reference page said every repo runs the guard on push to `main`. ut-website's CI has `push: branches-ignore: [main]` and so only runs on PRs and non-main pushes. | Fixed: the reference page now states the ut-website exception. ut-website's trigger is left as it is (repo-wide convention; every change reaches main through a PR). |
| 2 | info | In ut-my-shop and ut-admin-app the guard is a step after `npm run check` in the same job, so an earlier failure skips it. | Accepted: the job is red anyway, and that matches the other guards there. |

No high or medium findings.
