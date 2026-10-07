# Review: ambient join-discovery notice on a standalone till (ut-docs#2721)

**Card:** ut-docs#2721 (p1, `complexity:hard`). A new/upgraded till that
has no main till yet had no way to learn a shop's primary existed except
a manager remembering to click Settings → Tills → "Find a primary on this
network." Design: ADR-0033's amendment (`ut-docs` PR #3780, merged) —
the existing find→request→approve mechanics and manager-approval security
boundary are unchanged; the only new thing is *when* a browse happens and
a new dismissible UI surface.

## What shipped

- `internal/discovery/join_watch.go` (`JoinWatch`): finds and caches a
  LAN candidate for a standalone till, rate-limited to
  `PrimaryWatch.MinBrowseInterval` (5 min) as an independent timer. Does
  **not** reuse `PrimaryWatch` — that type's `PrimaryProof` needs an
  already-paired bearer an unpaired till doesn't have.
- `internal/pages/join_notice.go`: `GET /ui/join-notice` /
  `POST /ui/join-notice/dismiss`, mirroring the existing
  `GET /ui/pairing-notice` pattern. Skips rendering when this till is a
  replica, has enrolled replicas of its own (`TillsRepo.ListTills` —
  **found in Tester round 1**: a main till with real replicas was wrongly
  also getting offered to join a fresh till on the same LAN), the notice
  was dismissed, or there is no candidate.
- `web/ui/partials/join_notice.html` + `base.html` mount: a dismissible
  notice, HTML-escaped and `<bdi>`-isolated candidate name, "Link this
  till" posting straight into the existing `/api/sync/pair-start` form —
  no new pairing mechanics.
- Hooked into the existing 30s `syncPullTick` (every till runs this, not
  a new goroutine).
- i18n: `tills.discovery.ambient.{banner,link,dismiss}` in ar/en/fa/tr
  (de/es/pt are separate `ut-plugin-language-*` repos — follow-up PRs
  land in this same cycle). Help topic updated in all 5 languages.

## Review

Author: Opus 5.5 (two Dev rounds). Reviewer: Fable (different model),
one round plus one additional fix. Verdict: safe to merge.

**Found and fixed across the pipeline:**
1. (Tester) A main till with enrolled replicas also got the notice —
   fixed with the `TillsRepo` check above.
2. (Tester/UX) Till-name field had no visible label (placeholder-only);
   dismiss button was 28×22px, below this codebase's touch-target floor;
   a Latin till name broke mid-word in `fa`/`ar` at 360px — fixed: a real
   `<label>`, the shared `.notice-dismiss` class widened to 44×44
   (re-checked against the pre-existing `pairing-notice` — no layout
   regression), `<bdi>` + `overflow-wrap: anywhere` on the name.
3. (Reviewer) The in-flight pairing-wait fragment was rendered *inside*
   the 30s-polled mount, which gets innerHTML-swapped to empty whenever
   the handler has nothing to show (candidate gone after a lossy
   rebrowse, or a lapsed manager session) — this could silently delete an
   approved-but-not-yet-completed join mid-flight. Fixed: the status
   target now lives in `base.html` as a sibling, outside the mount;
   nothing swaps it but the form itself.

**Checked and accepted as-is (not bugs):**
- `hasEnrolledTills` runs only after a candidate is already found, so the
  common "nothing to show" poll costs no extra DB query over the
  pre-existing `/ui/pairing-notice`.
- Candidate name is attacker-controllable (mDNS TXT, any LAN host) —
  confirmed `template.HTMLEscapeString`'d before the `<bdi>` literal, with
  a test asserting a `<b>`-tag payload renders escaped.
- `POST /ui/join-notice/dismiss` requires `sync_management` (same gate as
  pair-start), tested with a 403 + no-persist assertion.
- Race safety mirrors `PrimaryWatch`'s existing `sync.Mutex` pattern;
  browse runs outside the lock.
- Offline-first: the browse is a bounded 3s call inside the existing pull
  goroutine; nothing on the checkout path is touched.
- `TillsRepo`'s `tills` table has no soft-delete (`DeleteTill` is a hard
  `DELETE`), so a shop that later revokes all its replicas correctly gets
  the notice again.
- No help-link precedent exists on the sibling `pairing-notice` either —
  consistent, not a new gap.

**Deferred (filed as Backlog follow-ups, matching the ADR amendment's own
explicit non-goals):** cloud-assisted candidate lookup for an unpaired
till; store-id confirmation; a warning when a till with its own real sales
history discovers a different main till; a picker for more than one
candidate; suppressing the ambient browse itself (not just the render) on
a main till that already has replicas.

## Verified

- `go build ./...`, `go vet ./...`, `gofmt -l .` clean.
- `go test ./internal/discovery/... ./internal/pages/... -race -timeout 60m`
  green (`internal/pages` alone runs ~37 minutes under `-race`).
- CI guard scripts (i18n, help-topics, help-drift, compliance-claims,
  competitor-naming, data-access, no-inline-handlers, no-showmodal,
  core-neutral, netaccess, page-http-error, htmx-loaded, kiosk-engine,
  demo-env, autofill, emoji-font, osk, plugin-menu-read, pipefail,
  card-data) all green.
- TDD re-verified independently (mutate → confirm the claimed failure →
  restore → green) for 8 of the new/changed checks: the enrolled-replica
  skip, the replica check, the dismissed-setting check (both in `Tick`
  and the handler), the rate limit, self-exclusion, the manager gate on
  dismiss, and the new status-target-outside-mount test.
- Real two-till LAN discovery driven end to end (Tester round): two local
  instances, mDNS found the standalone one's candidate, "Link this till"
  reached the real pair-start flow with a 6-digit verification code on
  one side and a pending-pairings row on the other.
- Screenshots looked at (not just rendered-HTML assertions): 1024×600
  kiosk floor and 360px phone, `en` and `fa` (RTL), before and after the
  touch-target/label/bdi fixes.
- Accepted gap: Playwright e2e not run (`e2e/node_modules` not installed
  in this container); no real physical-hardware two-till test. Both
  consistent with every other LAN-pairing card already shipped on this
  board (ut-docs#2325, #3575).
