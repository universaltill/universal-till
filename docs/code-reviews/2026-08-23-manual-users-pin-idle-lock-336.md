# Manual: self-service PIN change and idle auto-lock (ut-docs#336)

**Date:** 2026-08-23
**Repo/branch:** `universal-till` / `docs/336-manual-users-pin-idle-lock`
**Author:** Farshid Mirza (autonomous pipeline cycle) — Sonnet build, Opus review
**Card:** universaltill/ut-docs#336, `complexity:medium`

## What shipped

`web/help/en/users.md` (part of the illustrated-manual epic, ut-docs#324)
already covered the setup wizard, roles/permissions and manager elevation,
but two routes it already claimed in front matter — `/pin` (self-service
Change PIN) and the idle-lock control under `/settings` — had zero prose
anywhere in `web/help/**`. Confirmed by grep before writing (`idle` and
"change pin" both zero hits across the manual).

Added:
- `## Changing your own PIN` — who can do it, the 3-step flow, and the
  wrong-PIN/PIN-taken error paths.
- `## Idle auto-lock` — the timeout options and 10-minute default, that any
  input resets the countdown, the manager/admin gate on changing it, and
  the manual Lock button.
- Two new points in the existing "How to use it" list: a cross-link to the
  pre-existing `claim.md` topic (till claiming was already fully documented
  there, just never linked from here), and the top-bar Change-PIN/Lock
  affordance.
- `web/ui/pages/settings.html`: added the missing `{{ helpLink "users" }}`
  hint on the Auto-lock card (found by the independent review — see below).

## What the independent review found

Spawned a fresh-context **Opus** subagent (this card is `complexity:medium`
→ Sonnet builds, Opus reviews) with the full diff and explicit instructions
to verify every prose claim against the actual source, not take it on
faith, and to run the guards itself.

**Verdict: safe to merge, with 2 should-fix + 3 nitpicks.** No factual
inaccuracies were found — every claim about `ChangeOwnPIN`, the shared
device lockout, the idle-lock default/options, and the elevation gate was
checked line-by-line against `internal/auth/service.go`,
`internal/pages/auth_page.go`, `internal/pages/settings_page.go`,
`internal/pages/common/state.go`, `web/locales/en.json` and
`web/ui/partials/session_chip.html`, and matched.

| # | Finding | Severity | Resolution |
|---|---|---|---|
| 1 | Auto-lock card in `settings.html` had no `{{ helpLink "users" }}` hint — the page's own `?` therefore lands on `display.md`, not this topic, contradicting `CLAUDE.md`'s explicit "section inside an already-claimed page gets an explicit helpLink" rule, with 7 existing precedents in the same file. | should-fix | Fixed — added the hint, matching the established pattern exactly. |
| 2 | fa/ar/tr copies of `users.md` now lag this English-only addition. | should-fix | Accepted as out of scope — the card's own text says "English only on this card — translation is a separate card," and a real tracking card already exists open in Backlog: ut-docs#341 "Manual: translate into fa / ar / tr." Not re-filed; #341 already covers it. |
| 3 | Redundant Lock-button sentence (said twice, ~20 lines apart). | nitpick | Fixed — trimmed the first mention's repeated clause. |
| 4 | Terse sentence fragment ("10 minutes until someone changes it."). | nitpick | Fixed — reworded to match the surrounding prose's register. |
| 5 | "PIN already in use" doesn't distinguish active vs. deactivated operators. | nitpick, reviewer flagged as arguably not worth changing | Left as-is — reviewer's own assessment was that spelling out the distinction is developer-grade detail for an operator manual. |

## Verified beyond automated checks

- Built the till (`go build`), ran it against a fresh SQLite DB, and drove
  the actual first-boot wizard via curl end-to-end (language → country →
  shop name → shop type → restore-choice → admin PIN → landed on the sale
  screen).
- Confirmed the session chip's exact markup (`👤 Administrator` → `/pin`,
  Lock button → `/api/auth/logout`).
- Drove the real Change PIN flow: `POST /api/pin/change` with the wrong
  form, confirmed sign-out + redirect to `/login`, then confirmed the new
  PIN actually signs in.
- Fetched `/settings` live and confirmed the Auto-lock card's exact option
  list and default-selected value (10 minutes) against the prose.
- `make docs-shots` run for real (targeted `users`/`display` topics, then a
  full run after merging in a concurrently-merged PR — see below) against
  the environment's pre-installed Chromium; `node
  e2e/tests-docs/write-manifest.js` regenerated the manifest.
- Guards green: `guard-docs-shots.sh`, `guard-help-topics.sh`,
  `guard-i18n.sh`, `guard-compliance-claims.sh`, `gofmt -l .`, `go build
  ./...`. `go test ./...` not run — no Go logic changed (the one Go
  template line is data-only, already proven safe by the 7 existing
  identical-pattern usages plus a real Playwright render of the page during
  the docs-shots capture).

## Note on branch history

This branch's original base (`main` at the time of the initial commit) had
fallen behind `origin/main` by one PR (ut-docs#238, merged mid-cycle by a
different pipeline lane) between the start and end of this task. Merged
`origin/main` in, resolved the resulting `manifest.json` conflict by
regenerating it fresh rather than hand-merging the generated file, and
squashed the intermediate WIP commits (stop-hook-forced snapshots taken
mid-flight, per this pipeline's own guidance on never committing a
background regen mid-run) into one clean commit before push.

## Deferred / follow-up

- fa/ar/tr translation of the two new sections — tracked at ut-docs#341
  (pre-existing, not created by this cycle).

## Verdict

**Safe to merge.** `merge_method: "merge"` (never squash/rebase, per this
product's standing commit-attribution rule).
