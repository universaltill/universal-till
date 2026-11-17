# Code review — macOS install steps in the release notes (ut-docs#6024)

- Card: universaltill/ut-docs#6024 (`complexity:easy`, p3). Picked from the
  filtered Backlog by `lane:cloud-54`, because Ready had no buildable card.
- Branch: `fix/6024-macos-release-notes`.
- Pre-flight: release notes footer `updated`; `README.md` (install section +
  platform list) `updated`; `docs/arch/desktop-app.md` Signing & Gatekeeper
  `updated`; `web/help/` `n/a` (no help topic carries Mac install steps);
  `ut-website` `n/a` (download page already says Open Anyway, #3803; blog fix
  is #4127); `en.json` `n/a`; release.yml signing gap `deferred → #6025`.
- Process: Dev on Sonnet (fresh subagent, twice: build, then review fixes),
  Tester on Sonnet (separate subagent), Reviewer on Opus (separate subagent,
  different model from the author). BA/Architect folded into the Dev brief:
  the card's acceptance criteria were complete and the design was placement only.

## What changed

- `.goreleaser.yaml` `release.footer`: the macOS paragraph names
  `unitill-pos-*-macOS-arm64.dmg` as the download for Apple Silicon (Intel isn't
  built). It is signed and notarized, so it opens after the one-time
  "downloaded from the internet" prompt. The `.dmg` is attached shortly after
  publish. The portable `.tar.gz`'s `run-unitill.command` needs Privacy &
  Security → **Open Anyway** once. Removed: right-click → Open (macOS 15+
  ignores it) and "until we ship a signed build".
- New `scripts/ci/guard-release-footer-macos.sh` (+ `_test.sh`, 8 cases,
  fixtures only in `mktemp -d`) fails if:
  - `.goreleaser.yaml` contains right-, control-, ctrl- or secondary-click, or
    "until we ship a signed build";
  - it stops naming the `.dmg` or "Open Anyway".

  Wired into `ci.yml`'s `build` job.
- `README.md`: macOS install names the `.dmg`. The platform list says Apple
  Silicon only (it wrongly listed Intel).
- `docs/arch/desktop-app.md`: Signing & Gatekeeper now describes release builds
  as Developer ID signed and notarized, and ad-hoc builds as needing Open Anyway.

## Findings (Opus review)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | The footer says "notarized" without condition. With signing/notary secrets missing, release.yml still attaches an ad-hoc or un-notarized `.dmg`. | Footer now has an "if macOS still blocks it → Open Anyway" fallback. Root fix filed as #6025. |
| 2 | minor | The `.dmg` is attached after publish, so it is missing for up to ~90 min. | Fixed: the footer says it is added shortly after, and to use the portable copy meanwhile. |
| 3 | minor | Intel Macs got no guidance, and the README claimed Intel support. | Fixed: footer heading and README say Apple Silicon only. README install now names the `.dmg`. |
| 4 | minor | The guard missed Apple's own "Control-click" wording. | Fixed: regex extended, test case added. |
| 5 | nit | The positive checks (dmg, Open Anyway) match anywhere in the file. | Accepted: low value. The negative checks are the regression guard. |
| 6 | nit | `desktop-app.md` contradicted the new footer. | Fixed. `packaging/macos/*.sh` comments go to #6025. |
| 7 | nit | "unzip" for a `.tar.gz`. | Fixed ("extract"). |
| 8 | nit | ci.yml placement. | No change. |

## Verification

- Tester: the guard exits 1 on `git show HEAD~:.goreleaser.yaml` (old wording)
  and flags all four problems. A neutered guard (`exit 0`) makes the test report
  5 FAIL and exit 1, so the test is not a false pass. The rendered footer
  (YAML-parsed) matches `release.yml`'s dmg name.
- After fixes: the guard passes and its test is 8/8. Also passing:
  `shellcheck scripts/ci/*.sh` (0 issues), `guard-ci-tests-sandboxed.sh`,
  `guard-compliance-claims.sh`, `guard-mail-addresses.sh` and the YAML parse.
- Not run: `goreleaser check` (not installed in this container). CI's release
  wiring tests cover the file.

## Verdict

Approved. No blockers.
