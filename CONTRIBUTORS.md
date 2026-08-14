# Contributors

GitHub's contributor graph counts commit *authors* on the default branch,
not PR authors — so a contributor whose commits were authored under an AI
tool identity (a local git config mistake, see
[ut-docs#732](https://github.com/universaltill/ut-docs/issues/732)) shows
zero contributions there even with merged work in the history. This file is
the durable, correct record.

| Contributor | Notable merged work |
|---|---|
| [@farshid3003](https://github.com/farshid3003) | Product owner; core engine, fiscal/compliance, cloud, infra |
| [@pinsane](https://github.com/pinsane) | [#341](https://github.com/universaltill/universal-till/pull/341) — `catimport` header-matching fix; [#322](https://github.com/universaltill/universal-till/pull/322) — `docs-shots` CI guard narrowing |

Rewriting `main`'s history to fix the underlying commit-author field is
deliberately **not** done here — it would break every existing clone and
open PR to correct an attribution this file records just as well. New
commits are covered going forward by `.github/workflows/commit-attribution.yml`.
