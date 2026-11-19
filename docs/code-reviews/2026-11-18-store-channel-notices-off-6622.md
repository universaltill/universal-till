# Review — cloud notices stay off on store channels (ut-docs#6622)

Branch `fix/6622-store-channel-notices-off`. p1, security, complexity:easy.

## What shipped
On the `play` and `appstore` install channels (`internal/buildinfo/channel.go`,
ADR-0194), the cloud-notice inbox stays off whatever `notices.enabled` says:
a notice is free text from the cloud and a price or purchase pitch in it
cannot be filtered on the till. This is the till-side fallback; reporting the
channel to the cloud so it stops addressing those tills is a follow-up.
- `cloudsync` (`noticeClient.enabled`): no `notices` object in the POST
  check-in and no `notices=<rev>` on the GET, so nothing is pulled or stored.
- `pages.noticesEnabled` checks `accountLinkOutAllowed()` (channel AND the
  iOS platform rule) first, so `/notices` and every row/read/dismiss/ack
  route answer 404, and the bell, strip and toast render empty. Rows held
  from before are never shown.
- `POST /api/settings/notices` with `enabled=on` answers 404 on a store
  channel. Turning it off still works and still erases the held rows and
  the pull cursor.
- The Settings → Notices card (`{{ if accountlinkoutallowed }}`) and the
  `settings-notices` nav row are hidden.
- `web/help/en/notices.md`: one sentence saying Notices are not in the Play
  and App Store builds. docs-shots: `manifest.json` and the four
  `notices.png` kept, the other ~197 re-rendered PNGs reverted to `main`.

## Review (independent, Opus 5.5; code authored by Sonnet)
Verdict: **approve-with-fixes**. No blocker or major finding.

1. minor (test gap): `TestStoreChannel_NoticesStayOff` checked only that
   turning notices off answers 204 on a store channel, not that the held
   rows are erased → **fixed**: it now asserts `Pairs` is empty and
   `notices.enabled` is `false` after the turn-off. Reviewer confirmed the
   assertion fails with the `Reset` call removed.
2. minor (test gap): the store-channel gate on the GET check-in parameter
   (`checkin.go`, `notices=<rev>`) was untested → **fixed**: new
   `TestNotices_StoreChannelAddsNoGetParameter` (play, appstore and a
   sideload control that must carry the parameter).
3. nit: `settingsnav` import sat in the stdlib group of
   `store_channel_6388_test.go` (gofmt-clean but against goimports
   grouping) → **fixed**.
4. low (accepted, follow-up): `internal/data/agent_settings.go:149` still
   lists `notices.enabled` for the AI agent on store channels. It is
   explain-only (not `Writable`, not `RemoteWritable`), its deep link points
   at a card that is now hidden, and it carries no notice content. Even if
   the flag were set some other way, every reader ignores it on a store
   channel, so there is no policy exposure. This matches the precedent: the
   #6388 channel gating did not filter the agent catalogue either. Suggested
   epic checklist line: "agent settings catalogue: hide channel-gated
   regions on store channels".
5. nit (accepted): `cloudsync` gates on the channel only, while the pages
   gate on channel AND the iOS platform rule. An iOS binary that reports
   `sideload` (a dev build only; mobile builds fail closed to `appstore`)
   would still pull notices but never show them. That is acceptable, since
   no content reaches the screen.
6. nit (accepted): `ADR-0194 §3` in the new comments follows the existing
   `noticeBody` comment. Neither ADR-0194 nor ADR-0176 names notices on
   store channels explicitly. This is a narrowing in the spirit of
   ADR-0194 §2 (the channel decides which store-policy surfaces exist), not
   a change of course, so it needs no superseding ADR. A one-line note in
   ADR-0194 or ADR-0176 is a documentation follow-up.

Sideload and direct are unchanged: every new test runs all four channels,
and sideload and direct must keep the inbox, bell, strip, toast, card, nav
row, enable POST and check-in parameter. `accountLinkOutAllowed()` is true
on both, and the template only wraps the card in a conditional, so the
output is identical there.

docs-shots: keeping `manifest.json` while reverting the other PNGs is
consistent. The surface hash has to move, because `settings.html` and
`internal/pages/*.go` changed. The topic hash has to move, because
`notices.md` changed. On the sideload channel that docs-shots renders, the
change draws nothing different, so the reverted PNGs were byte-noise.
`guard-docs-shots.sh` passes.

TDD re-verification (reviewer, inline, revert → run → restore):
- `cloudsync` gate disabled: both cloudsync store tests fail on play and
  appstore.
- `noticesEnabled` gate, enable-404, nav-row and template conditional
  reverted: all three pages tests fail on play and appstore (GET /notices
  200, bell/strip/toast non-empty, POST enable 204, card and nav row
  present).
- Restored: everything passes.

Commands run: `gofmt -l internal/` (clean); `go vet ./internal/pages/
./internal/cloudsync/`; `go test ./internal/cloudsync/ ./internal/pages/
-run 'StoreChannel|Notices' -timeout 15m`; `go test ./internal/data/
./internal/buildinfo/`. All pass. `guard-docs-shots.sh`, `guard-i18n.sh`,
`guard-help-topics.sh` (fa/tr missing-topic warnings only, non-blocking)
and `guard-fixed-dates.sh` pass. The Tester's full gate (whole pages
package, golangci-lint, the other guards) passed before the review. The
review changed test files only, and those were re-run.

## Process
The card is easy, so BA, Architect and Dev ran as one Sonnet agent with
notes. No separate UX agent ran: the UI change only hides one card and one
nav row. The Tester's driven run checked that on a play build (card absent,
`/notices` and enable 404) and on sideload (card and nav row present, enable
204, `/notices` 200), without a screenshot. Reviewer: Opus 5.5.

Pre-flight: help topic `updated` (`web/help/en/notices.md`, English only,
ADR-0160); docs-shots `updated` (manifest surface and topic hash +
4 `notices.png`; unrelated PNG noise reverted); en.json/en.meta `n/a` (no
new key); reference page `n/a` (no ut-docs reference describes notice
gating by channel); ADR `n/a` (narrowing under ADR-0194, a doc note is
follow-up 6); README `n/a`; follow-ups → findings 4 and 6 (epic checklist
lines); review record — this file.
