# 2026-11-04 — e2e: plugin-view `confirm` component on an auth-on worker till (ut-docs#5466)

**Scope:** test support only, no product code. Split from ut-docs#4378. With
`UT_AUTH=off` core stamps no agent operator, so `action_propose` returns -2
(ut-docs#4168). Because of that, the `confirm` card had no browser run.

## What shipped

- `e2e/tests/worker-till.ts`: `startWorkerTill(…, { auth: true })`.
  - Drops `UT_AUTH` and uses its own port band (base + 150). It never reuses a listening till.
  - Completes first boot through the real `POST /api/auth/setup` with `redirect: 'manual'`. It requires the 303 and the `ut_session` cookie, so no PIN hash is seeded.
  - Proves auth is on: an anonymous `/` must redirect to `/login`. `/api/auth/setup` answers 303 even with auth off, so the 303 alone proves nothing.
  - Applies the worker defaults with the session cookie.
  - Callers that don't pass `auth` are unchanged.
- `internal/plugins/testdata/agent_guest`: a wasip1 guest.
  - On `ui.view.ask` it calls `ActionPropose("catalog.item.create.v1", …)` and returns `{type: "confirm", proposal}`.
  - The item name comes from `?name=`, so every open is unique.
- `e2e/seed_agent_plugin`: installs `com.example.e2e-agent`.
  - It writes an unsigned on-disk `manifest.json`. The manifest declares `ai.agent` and `ai.tools.write:catalog`. Granted permission rows, a `ui.view.ask` hook, a page and a fixture locale bundle are also seeded.
  - It is a separate plugin, so ADR-0153's data-class gate stays off `e2e-views`.
- `e2e/tests/plugin-confirm-5466.spec.ts` checks:
  - auth is on;
  - the card shows the intro text and the before/after table;
  - Confirm and Cancel are at least 44 px in en/fa at 1024 and 360 px;
  - Confirm creates the item on `/catalog`;
  - Cancel applies nothing;
  - the outcome notice replaces the card;
  - there is no horizontal scroll at 360 px in en and fa, before and after the swap.

  It signs in as a real manager on the PIN pad. It opens the view 7 times, and a counter fails it above 15 (cap: 20 proposals per session per hour).
- `e2e/README.md` gets a bullet for the `auth` option. `plugin-views-4378.spec.ts` gets a comment-only pointer.

## Review

Pipeline: Sonnet Dev, independent Tester, Opus review in a separate worktree.
Verdict: **approve with nits**. All six findings are fixed:

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | nit | seed header named the wrong plugin (`e2e-agent` for `e2e-views`) | fixed |
| 2 | nit | "Unlike seed_agent_plugin" named itself | fixed → `seed_plugin_views` |
| 3 | minor | `createManager` never asserted the user row, so a failed create showed only as a bare 240 s timeout | fixed: `toHaveCount(1)` |
| 4 | nit | the "auth is on" test had no `watchConsole` | fixed |
| 5 | nit | tag `cancel` is a prefix of `cancel-fa`, so `not.toContain` depended on test order | fixed → `cancel-en` |
| 6 | nit | worker-till comment said only "UT_AUTH=off" | fixed |

Checked and sound:
- No till process or temp-dir leaks.
- `os.MkdirAll` runs before every write.
- Ports 9241+ don't clash with other bands.
- The unsigned manifest is only parsed by `InstalledManifest`. The Ed25519 verifier runs at install, and no product code changed, so the production path is untouched.
- CI's plain `npx playwright test` runs the spec in the `default` project.

## Extra verification

- **Tester:** 7 separate mutations each failed the matching test:
  - confirm made a no-op;
  - cancel applies the change;
  - 30 px buttons;
  - fa overflow on the card, and separately on the notice;
  - `UT_AUTH` kept;
  - the auth check disabled.

  It also ran 15/15 on `--repeat-each 3`, 4/4 for the 5304 spec in the `auth` project, and 28/28 with no project filter. It took a screenshot of the fa 360 px card: RTL, stacked table, £۲٫۵۰, full-height buttons, no clipping.
- **Reviewer:** replaced `Action.Apply` with a fake success, and "confirm applies the change" failed at `/catalog`. With the file restored, the spec passed 5/5.
- **After the fixes:** the new spec plus the 4378 spec, 24/24 passed. Also:
  - gofmt, `go build ./...`, `go vet`, and the wasip1 vet of the guest;
  - `golangci-lint run ./...` with 0 issues;
  - guards: fixed-dates, i18n, mail-addresses, data-access, core-neutral, e2e-fixtures-import and the e2e-no-* guards.

Pre-flight: help topic n/a (test-only, no user-visible behaviour); reference/ n/a; README.md n/a (e2e/README.md updated with the auth option); en.json/meta/lang packs n/a (no core key; plugin fixture bundle only); ADR n/a (no new mechanism); plugin type/host fn/manifest field n/a (no manifest struct change, mirrors untouched); review record: this file; follow-ups: none.
