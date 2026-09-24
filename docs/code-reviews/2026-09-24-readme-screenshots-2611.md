# 2026-09-24: README screenshots (ut-docs#2611, README slice)

**Change.**
- The README gains a Screenshots section. Images live in `docs/images/` as WebP (1280 px wide). Till shots are v0.21.4 on the demo catalogue; Manage shop shots are the app on `main` with sample data.
- New `scripts/ci/guard-readme-local-links.sh` (+ `_test.sh`), wired into CI. It fails when a README or CONTRIBUTING link or image points at a missing repo file. It checks markdown `](…)` and HTML `href`/`src`, and skips code blocks, inline code, external URLs and #anchors.
- universal-till only: the README linked `CONTRIBUTING.md` four times but the file never existed; the guard found it. Added a short `CONTRIBUTING.md` built from `CLAUDE.md` and CI facts.

**Tests.** The guard's regression test covers a missing markdown image, a missing `<img src>`, and valid files plus external links plus code examples. It passes on the real README. Before CONTRIBUTING.md was added, the guard failed on the real README. shellcheck is clean, with targeted SC2016 suppressions for literal backticks.

**Review.** Independent review by a different-model subagent (Sonnet), read-only. All four findings fixed:
1. The public README linked the private `ut-my-shop` repo. Now links my.universaltill.com.
2. CONTRIBUTING gave the wrong translations path. It is Menu → Administration → Translations (ut-docs#2008).
3. The guard could false-positive on links inside code. Code blocks and spans are now stripped, with a test.
4. "merged with a merge commit" read as enforced. Reworded as the project's practice.

Also verified: every CONTRIBUTING claim (make targets, locales, public plugin repos, commit-attribution behaviour) is accurate, ci.yml runner placement is correct (public repo on ubuntu-latest), and the images contain demo data only.

**Exposure.** Images show demo data only. No private links from the public repo.
