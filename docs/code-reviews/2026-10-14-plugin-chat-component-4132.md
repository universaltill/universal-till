# Review — plugin-view `chat` component and core-carried `_turns` (ut-docs#4132)

**Shipped.** ADR-0121 2026-10-11 amendment, items 2–4. Item 1 (rail placement, `ui:rail`, `ai_chat`) is #4131.

- **`chat` component.** It joins the plugin-view vocabulary (`internal/pluginview/chat.go`). Core draws the transcript in its own template (`pluginview_chat` in `web/ui/partials/pluginview/controls.html`):
  - user turns at the end edge, assistant turns at the start edge;
  - tool-use chips under an assistant turn;
  - a question box (`question`, at most 1000 characters) with Send.
- **Validator.** It refuses the whole document for:
  - an unknown field, or a second chat;
  - a bad role, more than 10 turns, or more than 8 KiB of turn text;
  - a non-literal turn, or a turn over 4 KiB;
  - more than 8 tools, or tools on a user turn;
  - a bad action;
  - a chat in a core seam.
- **History.** Core carries it in a hidden `_turns`: role and text only, at most 10 turns and 8 KiB serialized, oldest dropped first.
  - On a post, `DecodePost` takes `_turns` out before `DecodeForm`, which still refuses every other `_` field.
  - It validates `_turns` and hands it to the plugin as `turns` in `ui.action.ask` and the job payload. A malformed value becomes `turns: []`, never an error.
  - Both the urlencoded and multipart paths do this.
- **Thinking state.** A job started from a chat post keeps an interim on its in-memory job record: the posted turns, the question and the action.
  - Every poll draws the transcript plus a thinking bubble that is the poll itself.
  - A failed job keeps the transcript, shows the action-failed notice, and refills the question box.
  - Nothing goes to the DB, a cache or plugin storage. The interim goes when the job goes.
- **i18n.** 8 `plugin.chat.*` keys in en/ar/fa/tr. The de/es/pt packs get follow-up PRs on branch `i18n/4132-chat-keys`.
- **Accessibility and bidi.** The transcript is `role="log" aria-live="polite"`. Each turn carries `dir="auto"`, so an English answer reads correctly on an fa till. The question input uses `unicode-bidi: plaintext`.
- **Docs.** `ut-docs reference/plugin-views.md` documents the component, the `turns` payload and the limits. The component is out of "Not yet shipped".

**Process.** Built by Opus 5.5 as a TDD-first Dev subagent. Tested by a separate Opus 5.5 Tester subagent. Reviewed independently by Fable.

**Tester.**
- **Mutation audit.** About 35 single-check mutations, each caught by a test. Three tests were too weak and have been fixed:
  - the "`_turns` posted twice" order, in both packages;
  - a turn with both `key` and `literal`.
- **Full-suite failure fixed.** `internal/auth` `TestEveryPollerIsClassified` failed because the new thinking-bubble poller was missing. It is now listed as a request poller (the same `?_job=` poll as `poll.html`).
- **Bidi bug fixed.** Latin punctuation was misplaced in English turns on an fa till. The fix is `dir="auto"` on turn text, test-first.
- **Driven run.** The real `servePluginView` with the fake plugin bus and the real assets, in Chromium:
  - en/de/fa, light and dark, at 1024×600 (windowed and kiosk) and 360×800, in the normal, thinking, failed and answered states;
  - 25 geometric assertions, all passing: bubble edges (mirrored in RTL), no horizontal scroll, targets ≥ 44 px, newest turn in view;
  - a real htmx type → Send → thinking → answer flow in en and fa.
- **Screenshots.** Read by the Tester, and by the orchestrator for fa 1024×600 and thinking at 360×800.

**Review findings (Fable).** No blockers and no majors.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | `turns` and the chat interim key off `_turns` being posted, not off the action being a chat's. A hand-edited post can turn a non-chat job's poll into a chat UI. It only affects the operator's own page. | **Accepted.** Docs now say `turns` is present whenever `_turns` was posted, and to treat it as operator input. |
| 2 | minor | The question was unbounded up to the 64 KiB form cap. A plugin could not echo a question over 4 KiB back as a turn, so its next document was refused. | **Fixed.** `maxlength="1000"` on the input (≤ 4 KiB of UTF-8), with a test (failure confirmed without the fix) and a docs line. |
| 3 | minor | No test for a chat in the overlay or a slot panel. | **Fixed for the overlay.** `TestPluginChat_InOverlay_4132` covers GET and POST targeting `#plugin-overlay-view`; a broken assertion was confirmed to fail. The slot path shares the same `.TargetID` logic, which the reviewer traced as correct. |
| 4 | nit | The whole transcript is re-announced when job progress changes. Not every second: unchanged polls answer 204. | **Accepted.** Progress messages are rare. Revisit with streaming (#2840). |
| 5 | nit | Docs: `_turns` 8 KiB counts escaped JSON, so heavy quoting posts back fewer turns than were drawn. | **Fixed.** One sentence in the docs. |
| 6 | nit | The `stillRunning` test helper matches the substring `plugin-chat-thinking`. | **Accepted.** Test-only. |

**Reviewer's checks that passed:**
- Escaping in every attribute context, including the hidden `_turns`.
- A forged `_turns` or `question` cannot cause a 500 or unbounded memory.
- Job route/plugin ownership is unchanged, and the interim is pruned with the job.
- `-race` runs clean on the chat, job and view tests.
- Document and redirect answers to a chat post.
- Logical CSS only, and no `showModal`.

**Not verified.**
- Real touch hardware and a real kiosk Pi (touch was emulated).
- ar and tr were not screenshotted.
- No Playwright e2e test plugin for plugin views exists, so the driven run is not repeatable in CI.
- On a phone, Send sits below the fold under a long transcript. The page scrolls, and Enter also submits.

**Verdict.** Safe to merge.
