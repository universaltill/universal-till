# Code review — docs-shots resolver finds the new Playwright headless-shell layout (ut-docs#3257)

- **Date:** 2026-10-02
- **Ticket:** ut-docs#3257 (`complexity:easy`, bug, dev tooling)
- **Branch:** `fix/3257-resolve-chromium-headless-shell-layout`
- **Author:** Opus 5.5 (cloud lane `:24`, built inline).
- **Reviewer:** independent fresh-context Fable subagent (a different model
  from the author, per `MODEL-ROUTING.md`).
- **Verdict: SAFE TO MERGE.** No blockers. Two minors and one nit fixed; one
  minor kept as is, with the reason below.

## The gap

`e2e/scripts/resolve-chromium.sh`'s `find_headless_shell` only matched the
old layout, `chromium_headless_shell-*/chrome-linux/headless_shell`, and
only searched `PLAYWRIGHT_BROWSERS_PATH` and `/opt/pw-browsers`. Current
Playwright (headless shell v1228, which `e2e/package-lock.json` installs)
puts the binary at
`chromium_headless_shell-1228/chrome-headless-shell-linux64/chrome-headless-shell`
in `~/.cache/ms-playwright`. So on a fresh developer machine the resolver
found nothing. `docs-shots.sh` then ran `playwright install --with-deps`,
which runs `sudo apt-get` and waited for a password nobody could type.

## What shipped

- `resolve-chromium.sh`: `find_headless_shell` matches
  `chromium_headless_shell-*/chrome-headless-shell-*/chrome-headless-shell`
  first, then the old layout. Searches Playwright's default cache
  (`${XDG_CACHE_HOME:-$HOME/.cache}/ms-playwright`, or
  `~/Library/Caches/ms-playwright` on macOS) after `PLAYWRIGHT_BROWSERS_PATH`
  and before `/opt/pw-browsers`. This is the same order Playwright uses.
  `set -u` stays safe when `HOME` or `XDG_CACHE_HOME` is unset.
- `docs-shots.sh`: when no browser resolves, a non-root user with no stdin
  TTY and no passwordless sudo now stops with a message that names the two
  ways out. Before, the run hung on the sudo prompt.
- `resolve-chromium_test.sh`: the test sets `HOME` to a temp directory, so
  a developer's own cache can't change results. Four new cases cover the new
  layout under `PLAYWRIGHT_BROWSERS_PATH`, under `~/.cache/ms-playwright`,
  and under `XDG_CACHE_HOME`, and check that the new layout wins over a
  stale old-layout revision in the same root. Also fixed the file's SC1007
  shellcheck warnings (`VAR=` → `VAR=''`, same meaning).

## Findings

1. **minor — fixed.** In the first version, the old-layout glob came first,
   so a stale `chromium_headless_shell-1194` beside the current 1228 won.
   That is likely on a machine where someone bumped `@playwright/test`
   without uninstalling. The new layout now comes first, and there is a test
   case for it (red with the old order, green now).
2. **minor — fixed.** Header comments in both scripts still said developer
   machines never take the reuse path. Updated: developer caches now take
   it, and the version-mismatch warning is what tells them it's behind the
   pin.
3. **minor — kept.** The fail-fast checks stdin (`-t 0`), but sudo prompts
   on `/dev/tty`, so `make docs-shots </dev/null` from a real terminal fails
   fast even though sudo could have asked for the password. Kept on purpose:
   the hang this card is about happened in an agent session that *had* a
   controlling terminal with nobody at it. A `/dev/tty` check would not have
   stopped it. The reason is now a comment in the script.
4. **nit — fixed.** Comment line wrap.
5. **nit — pre-existing, not changed.** `PLAYWRIGHT_BROWSERS_PATH=0` (the
   node_modules-local browsers) is not translated. It just falls through to
   the other candidates.
6. **nit — pre-existing, not changed.** `resolve-chromium_test.sh` isn't
   wired into CI. It needs a pre-installed browser plus `npm ci`, and skips
   cleanly without them.

The reviewer also checked that the fail-fast does not fire on GitHub Actions
`ubuntu-latest` (runner user with passwordless sudo) or when running in
Docker as root. `docs-shots-determinism.yml` is the only CI caller of
`docs-shots.sh`.

## Verification beyond automated tests

- **TDD re-verified** (reviewer, in a separate worktree): with only the
  resolver reverted to `origin/main`, the three new-layout cases fail and the
  older cases pass; restored, everything passes. After the review fixes,
  reverting only the glob order makes the mixed-root case fail (the stale
  1194 resolves); restored, all 11 cases pass.
- **Driven run of the fail-fast:** ran the guard as `nobody` with no TTY
  (`setpriv --reuid=65534`): prints the message and exits 1. As root: falls
  through to the install. The resolver on this box (no `~/.cache`) still
  resolves `/opt/pw-browsers/chromium_headless_shell-1194/...`, with
  `PLAYWRIGHT_BROWSERS_PATH` set and unset.
- `shellcheck` on all three scripts: 0 issues.
