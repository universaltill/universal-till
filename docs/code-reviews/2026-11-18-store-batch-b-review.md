# Review — store batch B: Play flavor + publish, App Store bundled payments, installer publish, marketplace links (2026-11-18)

PRs universal-till#2586 (ut-docs#6409), #2585 (#6410, #6405), #2587 (#3433, #6565),
#2590 (#6407), #2593 (#6400), and the baseline-only #2601. One combined Tester and
one combined Reviewer (Fable) for the whole batch, full `go test ./...` once on an
integrated tree (all branches merged on main): no failure.

## Why the PRs were red
Mostly drift and a few small real defects, no single shared cause:
- Merge drift: #2585/#2586/#2587/#2593 conflicted with main (android-ci.yml, MainActivity.kt,
  README, i18n meta, docs-shots manifest, ios-testflight.yml). Resolved by union; the manifest
  regenerated with `node e2e/tests-docs/write-manifest.js`, never by hand.
- #2586 `compile`: a Maven Central 429 (flake, re-run).
- #2585: shellcheck (SC2015/SC2016/SC2181) in four test scripts and the pipefail early-exit-reader
  guard (ut-docs#2946) in play-publish.yml and its tests.
- #2590: the same pipefail guard in publish-installer.sh; `release-provenance_test.sh` (ADR-0181)
  allows id-token on exactly four jobs and the new publish-installer job is the fifth.
- #2587: main renamed `buildinfo.Channel` (type) to `InstallChannel`; three files and the guard
  regression test still used the old name; `plugins.CompareVersions` was unused (deadcode guard);
  `bundled.OverrideAvailableForTest` is a test seam baselined in #2601.
- Reviewer B3: #2586 and #2587 were based on the already merged `feat/6388-store-channel`; retargeted to main.
- Reviewer B5: #2586 alone could not compile the play flavor with main's TapToPayShell
  (no `TAP_TO_PAY_ENABLED`, no ttpStub source set for `play`); ported from #2585.
- Reviewer B4 (ADR-0194 §3): #2593 linked the marketplace terms/guidelines to the full-chrome
  website pages on every channel; now the chrome-less `/app/legal/` view like the About page.

## Early-review findings (Fable, 2026-11-17)
Fixed on these heads: #6 notice purchase link (merged earlier, #2583), #10, #11, #12, #16, #17, #18,
#19, #6626 (tax-uk is in the bundled lock), #6627 (bundled plugins get their declared permissions).
Open: #2/#6623 for `release-signing` (a `v*` tag reaches the signing and installer-publish
identities; Play is gated), #20 (bundled wasm provenance; CODEOWNERS inert), #6624 (ut-infra).

## Follow-ups (not blockers)
F1 installer publisher identity behind `release-signing` (part of #6623). F2 play AAB Go core
linker-stamped `sideload` while the runtime channel is `play`; play-publish does not run
`check-artifact-channel.sh`. F4 App Store main till and a non-App-Store satellite's VAT plugin.
F5 determinism: `backups-card.png` differs between runs (nothing in this batch touches it).
The marketplace terms/guidelines pages do not exist on the website yet (ut-website#102, owner-gated).

## Verdict
Blockers B1-B5 fixed and re-tested; merge order #2601, #2590, #2586, #2585, #2587, #2593.
