# Review: a card's own refusal text takes down the generic server-error banner (ut-docs#3247)

**Shipped.** Settings cards whose forms write a refused save's body into
their own message span (`text-response:<id>` — Staff languages 400,
Display mode 400, Report retention 409, Till name 422) still left the
page-wide `#pos-alert` "server error" banner up: `app.js`'s
`htmx:responseError` listener raises it for every non-2xx, and only the
`save-error` action ever took it back down. `save-error`'s banner-hiding
block is now `hideGenericServerAlert()` in `web/public/inline-actions.js`;
`text-response` calls it when it wrote a non-empty message. An empty body
leaves the banner up (nothing else to read). It only hides the banner while
it still shows the generic server text, never a network-error alert. No Go,
template, string or help-topic change: the cards' own messages were already
translated (ut-docs#2982), and the manual doesn't describe these banners.

**Tests.** New `e2e/tests/settings-staff-languages-refusal-3247.spec.ts`:
a real server refusal (no stub — the hidden default-language input is
dropped, the server answers 400 "Select at least one language.") must
leave `#pos-alert` hidden; a stubbed empty 400 must leave it up.
`settings-till-name-taken-3308.spec.ts` gains the same banner assertion.
Helper `recordAlertAfterRequest` (`e2e/tests/helpers.ts`) records the
banner state at `htmx:afterRequest`, because app.js self-heals the banner
on the next unrelated 2xx (status poll) and a live `toBeHidden()` alone
passed on the bug.

**Reviewer:** independent Opus 5.5 subagent (author: Sonnet), fresh context,
separate worktree. It ran build, the guards that scan web/public JS and
e2e (`guard-i18n`, `guard-no-showmodal`, `guard-e2e-no-browser`,
`guard-e2e-fixtures-import`, `guard-no-inline-handlers`,
`guard-htmx-loaded`), checked htmx 1.9's event order (responseError fires
synchronously before afterRequest, so the hide always runs after the show),
and re-verified TDD: with only `inline-actions.js` reverted, both banner
tests fail (`Expected: true / Received: false`), the empty-body control
passes.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | low | `text-response` hides the banner without checking `X-UT-Response: refused`; display-mode's invalid-`mode` 400 is an untranslated `http.Error` (unreachable from the UI's radios), retention's 409 is translated but unmarked | accepted; follow-up ut-docs#3978 (RefuseText both, then gate on the header) |
| 2 | low | The helper's 50 ms delay let an unrelated 2xx self-heal the banner first, so a regression could pass | fixed: read synchronously (inline-actions runs in the capture phase on `document`, before the helper's `body` listener); orchestrator re-ran the revert check — both tests still fail without the fix, pass with it |
| 3 | nit | Does `addInitScript` leak across tests? | no — fresh context per test |

**Verified beyond automated tests:** none on hardware; this is a client-side
banner state on the Settings page, driven in headless Chromium. Not looked
at visually beyond the e2e run (no layout change).

**Verdict:** safe to merge.
