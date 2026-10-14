# Review — recovery screen: no safe-mode link a peer can't open (ut-docs#4290)

**Shipped.** Since ut-docs#4268 the recovery server sits behind the ADR-0157
LAN guard (loopback, the till's own interface addresses, `UT_UI_ALLOW_CIDRS`),
while the safe-mode routes keep the stricter loopback-only `requireLoopback`.
An admitted non-loopback peer (e.g. the host browser of a Docker till with
`UT_UI_ALLOW_CIDRS=172.16.0.0/12`) was shown the "Today's sales" link and got
a 403 page. Now:

- `internal/recovery/safemode.go`: `isLoopbackPeer(r)` (RemoteAddr only,
  never a forwarded header) extracted from `requireLoopback`, whose
  behaviour is unchanged.
- `page.go` / `templates/recovery.html`: the link renders only for a
  loopback peer; any other admitted peer gets a one-line note
  (`recovery.safe_mode_this_device_only`) instead. Safe mode stays
  loopback-only (card's recommended option 1 — keeps read access to sales
  data on the device itself).
- New key in en/ar/fa/tr; packs de/es/pt follow in their own PRs.
- Manual: `web/help/{en,de,ar,fa,tr}/recovery.md` safe-mode bullet says the
  link only appears on the till itself.

Built by Sonnet (complexity:easy), reviewed by Opus 5.5 in a fresh context
on a separate worktree.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | minor | `web/help/ar/recovery.md` not updated (structural drift guard can't see it) | Fixed — sentence added |
| 2 | minor | A browser on the till itself pointed at its LAN IP (not localhost) is admitted by the guard but not loopback, so it sees "only on the till itself" while on the till | Accepted — every shipped shell (desktop, Android, Pi kiosk) opens `127.0.0.1`; the case needs someone typing the LAN IP on the till, and the previous behaviour there was a 403. Not worth a longer string in 8 languages |
| 3 | minor | ar used "الصندوق" for till; neighbouring `recovery.*` keys use "الكاشير" | Fixed |
| 4 | nit | fa "فقط‌خواندنی" (ZWNJ) vs the banner's "فقط خواندنی" | Fixed — matches the banner |
| 5 | nit | test cleanup resets the translator to `InitI18n(nil,"en")` rather than the prior one | Accepted — no parallel tests in the package; `Serve` re-inits it |

Security: the change only hides the link from more peers; nothing becomes
reachable that wasn't. `isLoopbackPeer` ignores `X-Forwarded-For` (tested);
`::ffff:127.0.0.1` counts as loopback, correctly.

## Verified

- TDD: `TestPage_AllowedLANPeerGetsNoteNotSafeModeLink` fails on the
  pre-fix page/template ("LAN peer is offered the loopback-only safe-mode
  link") and passes with the fix — re-verified by the reviewer in its own
  worktree.
- Tests: allowed-CIDR peer gets note, no link; loopback peer gets link, no
  note; safe mode unusable → neither; `TestIsLoopbackPeer` (v4/v6 loopback,
  LAN IP, malformed, spoofed XFF).
- Gate: gofmt, `go build ./...`, `go vet ./...`, `go test ./...`,
  golangci-lint (recovery), guard-i18n, guard-help-drift, guard-help-topics,
  guard-compliance-claims, guard-competitor-naming, guard-no-showmodal,
  guard-data-access, guard-core-neutral, guard-kiosk-engine.
- Looked at: the rendered page for an allowed-CIDR peer in en at 1024×600
  (note sits in the safe-mode section under the divider, no overlap) and fa
  (`dir="rtl"`, Persian note). Not looked at: ar render, 360px (headless
  capture clipped the pre-existing card layout, unchanged by this diff),
  real hardware. No e2e covers the recovery screen; no help screenshot
  exists for it.
- Packs: `validate.sh` + `check-key-drift.sh` for de/es/pt pass against
  this branch's `en.json` (3287/3287 keys).

**Verdict:** safe to merge.
