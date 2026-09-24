# Contributing to Universal Till

Thanks for helping. This page covers what a pull request needs to get merged.
`CLAUDE.md` in this repo lists the full coding rules. Most of them are
enforced by the CI guards under `scripts/ci/`.

## Workflow

1. Fork the repo and create a feature branch. Never work on `main`.
2. Write a failing test first, then make it pass.
3. Run `make test` (and `make e2e` if you changed anything a user sees).
4. Open a pull request. We merge pull requests with a merge commit, not a
   squash.

**Commit author must be you.** Use your own name and a GitHub-verified email
address; `<id>+<login>@users.noreply.github.com` works well. If an AI tool
helped, credit it in a `Co-Authored-By:` trailer. The
`commit-attribution.yml` check rejects commits authored by an AI tool
identity, because GitHub would then credit nobody.

## Rules CI checks

- **Offline first.** A sale must complete with no network. Sync and cloud
  are extras, never requirements.
- **Money** is `internal/money.Money` in integer minor units. Never use
  floats.
- **SQL** lives only in `internal/data` (repositories) and `internal/db`
  (migrations).
- **Migrations are append-only.** Never edit a merged migration file; add
  a new one.
- **No hard-coded user-facing text.** Templates use `{{ T "key" }}`, and
  every key must exist in every `web/locales/*.json` file (`en.json` is the
  base).
- **Right-to-left support.** Use logical CSS properties
  (`margin-inline-start`, `text-align: start`), never left/right.
- **Compliance wording.** Describe what the software does. Never claim a
  certification.

## Translations

The till ships English, Turkish, Arabic and Persian in `web/locales/`. Other
languages are separate language-pack plugins, for example
[ut-plugin-language-de](https://github.com/universaltill/ut-plugin-language-de)
and [ut-plugin-language-es](https://github.com/universaltill/ut-plugin-language-es).
To add a language, start from `web/locales/en.json` and open a pull request
for a new `ut-plugin-language-<code>` pack.

A translation must mean the same as the English. Having every key present is
not enough. Shop owners can also change any wording on their own till under
**Menu → Administration → Translations**.

## Plugins

New features are plugins by default, each in its own repository.
[ut-plugin-faq](https://github.com/universaltill/ut-plugin-faq) is the
smallest working example to copy. Plugins are signed, and the till verifies
every plugin before running it.

## Screenshots

`make docs-shots` regenerates the help-page screenshots under
`web/help/img/`. Run it and commit the result whenever you change a screen.
README images live in `docs/images/`, and
`scripts/ci/guard-readme-local-links.sh` checks that every local link and
image in this README exists.
