# Code review — pairing code is opaque, not raw JSON (board issue #7)

- **Date**: 2026-07-30
- **Branch**: `fix/pairing-opaque-code`
- **Card**: ut-docs issue #7 (first card taken through the new GitHub
  Projects board pipeline).
- **Scope**: Farshid's field report from the Pi5 install — the multi-till
  pairing "code" showed raw JSON `{"url":…,"token":…}` on screen and the
  entry point was buried. Interim fix (the real solution is the queued
  zeroconf LAN auto-discovery / approve-to-pair item, which removes manual
  codes entirely).
- **Independent review**: different-model (opus) subagent — findings below.

## What changed

- `internal/pages/sync_api.go`: the enrolment payload (primary URL +
  one-time token) is now packed into ONE **opaque base64url code**
  (`encodeEnrollCode`) for both the QR and the manual `<code>` element,
  instead of rendering raw JSON. `decodeEnrollCode` reverses it and still
  accepts a raw-JSON payload from a not-yet-upgraded primary (base64url
  can't contain `{` or `"`, so the two forms never collide). `joinPrimary`
  decodes via the helper.
- `web/ui/pages/tills.html`: the join-code input placeholder is now an
  i18n key (`tills.join_code_ph`) instead of the literal JSON example.
- `web/ui/pages/settings.html`: a dedicated "Tills" card (with a clear
  "Manage tills / pair a new one" button) replaces the link that was
  buried in the Printer card's button row — so pairing is findable.
- `web/locales/{en,ar,fa,tr}.json`: 3 new keys, translated per locale.

## Verification

- **TDD, failing-first**: `TestEnrollCode_RoundTripOpaque` (asserts the
  code contains none of `{`/`}`/`"`/`token`/`http`/`url` and round-trips),
  `TestDecodeEnrollCode_LegacyRawJSON` (back-compat), and
  `TestDecodeEnrollCode_Rejects` (empty/garbage/missing-field) — all seen
  failing (helpers undefined) before implementation.
- The existing full-flow handler test + `issueEnrolCode` helper encoded
  the OLD raw-JSON behavior; both were flipped to extract the opaque code
  from the real rendered `<code>` and assert it is NOT JSON — so they now
  guard the fix through the real mux + template.
- Full `go build ./...`, `go vet ./...`, `go test ./...`,
  `guard-data-access.sh`, `guard-i18n.sh` — all green.
- **Real driven run** (built binary, UT_AUTH=off): `POST
  /api/sync/enroll-token` renders `<code>eyJ0b2tlbiI6…</code>` (opaque,
  decodes to the right url+token); `/settings` shows the new Tills card;
  `/tills` returns 200 (new placeholder key resolves). Server stopped
  after.

## Independent (opus) review findings

**No blockers.** Both headline claims confirmed with fresh probes:
- **No leak** — `encodeEnrollCode` output is pure base64url `[A-Za-z0-9_-]`
  across 4 inputs (incl. IPv6 host, `-`/`_`/`~` in the token); none of
  `{ } " : / . token url http` appear. Issue #7's screen-leak is closed.
- **Back-compat** — an upgraded replica reading an old primary's raw-JSON
  QR still works: JSON starts with `{` (not in the base64url alphabet), so
  base64url decode errors and control falls through to the raw-JSON branch;
  no collision either direction. Verified.
- **Revert-probe** — making `encodeEnrollCode` return raw JSON made both
  `TestSyncEnrollTokenAndEnroll_FullPairingFlow` and
  `TestEnrollCode_RoundTripOpaque` FAIL (`leaks "{"`), then restored — the
  tests are genuinely load-bearing, not false-passes.
- i18n keys real & distinct in all 4 locales; guard passes; RTL card uses
  only logical/inherited direction — fine. `encoding/json` still used;
  no stray `payload`; build/vet clean.

**Findings, none blocking this interim fix:**
- *should-fix (pre-existing, out of scope here → queued as a new card):*
  `joinPrimary`'s error strings (`sync_api.go` ~345–401) are hardcoded
  English and rendered raw into the HTML on `/api/sync/join`, while the
  success path is i18n'd — so a mis-paste shows English on an ar/fa/tr
  till. Predates this change and spans the whole function; logged as a
  follow-up board card rather than ballooning this fix.
- *caveat (release note):* mixed-version rollout — an OLD replica pasting
  a NEW opaque code fails (it json.Unmarshals directly). Upgrade replicas
  with/before primaries. The reverse (new replica ← old primary) works via
  the fallback. Unavoidable direction of a one-sided format change.
- *nits:* QR is ~33% denser (still well within capacity at size 220);
  padded/standard-base64 codes aren't accepted (our codes are always
  unpadded — no practical impact).

Reviewer's advisory: this is NOT wasted vs. the future zeroconf
auto-discovery — a manual paste-a-code fallback + the Settings entry are
still needed when discovery fails or across subnets, and the
encode/decode pair can back an approve-to-pair handshake. No version byte
needed (JSON payload is already forward-extensible).
