# Review: pending link, joining-till side (ut-docs#5330, slice B2)

Status: **reviewed: ship after fixes. The fixes are not done yet.** The card hit its
complexity:hard budget (150 min) after the review and before the fix-up
pass. The open findings are listed under "Required before merge" and the card
waits in Admin Review (`budget-exceeded`). Part of ut-docs#5330; this branch
must not close the card until those fixes land.

## What the change does
- `internal/fleetlink`:
  - `WatchPending(ctx, PendingTarget, PendingOptions) PendingResult` dials the main
    till's no-bearer pending link (B1, PR #2313) over the pinned TLS client.
  - It answers pings, reads at most 4 KiB, backs off with full jitter from 1 s to 30 s,
    and honours Retry-After.
  - Final statuses and close codes end the watch.
  - The result carries integers only and no error text.
  - The bearer-only `dial` is split into a shared `dialWith`.
- `internal/pages`:
  - A `pendingLinkRunner`, registered once at Init on `bgCtx`/`wg`, owns one goroutine
    per attempt.
  - `rp.set` and `rp.finish` stop the previous attempt's link.
  - When `pending_link` is set, the per-id GET sends the secret only in
    `X-UT-Pair-Request-Secret`.
  - The upstream GET follows a throttle: approved → at once, then every 5 s; link up →
    every 60 s; still connecting → every 15 s.
  - A deny or expiry over the live link renders `tills.pairing.no_response` at once.
- `web/ui/partials/pairing_wait.html`:
  - Polls `every {{ .pollEvery }}`: 1 s with a live link, 15 s otherwise.
  - A `have` key in `hx-vals` lets an unchanged poll answer 204, so the
    `aria-live` host in setup.html is not re-announced every second (UX gate).
- `web/help/{en,de,tr,fa,ar}/multitill.md` step 3 gets one sentence. No new i18n keys.

## Independent review (Fable; code written by Sonnet)
Verdict: ship after fixes. Nothing blocks on security grounds.

**Checked and sound:**
- The secret travels only in headers.
- The dial is wss-only, and the pin is enforced in VerifyConnection before any byte.
- Redirects are refused on both clients.
- Logs carry integers only.
- `wg.Add` runs under the mutex before the closed/Wait check, and no atomics are copied.
- Reader goroutines are joined.
- The TTL comes from `WithTimeout`.
- The `have` key cannot suppress a terminal render.
- htmx 1.9.12 keeps the `every` trigger on a 204.
- The pinned client is HTTP/1.1 only with Timeout=0 (test F3).

Findings:
1. **minor, open.** A protocol end calls `conn.Close(CloseProtocol)`, which is the close
   handshake: up to 5 s, and it ignores ctx (`pending_client.go` ~136-141,
   178-215). `rp.finish()` → `stop()` waits on it while holding `rp.mu`
   (`pairing_join.go` ~183-186). A hostile or broken main till can therefore stall
   every pair-status poll for up to 5 s. Fix: `CloseNow` for a protocol end, plus
   a test that `stop()` returns fast.
2. **minor, open.** The main till's `out.Data.ID` is not validated as a header value or
   path segment (`pairing_join.go` ~479-488). A control character or a huge id
   turns every dial into a transport error and a redial until the TTL. This is bounded
   and leaks nothing. Fix: validate at pair-start.
3. **minor, open (test quality).** No test checks that `rp.finish` stops the watch on
   pairStatusHandler's four error branches (status not 200, bad body, has-sales,
   completeJoin error). Fix: add one table test.
4. **nit.** An oversized ping id is dropped silently. It should get a code comment.
5. **nit, accepted.** The help's "up to about 15 seconds on a slow connection"
   simplifies the refused-link case and the 60 s safety-net case. UX approved
   the wording.

## Tester (Sonnet, separate agent)
- **Mutations: 5 of 5 killed.**
  - Secret in the URL: killed by P1_P2 and P4.
  - Approval never fetching the token: killed by P1_P2 and P6.
  - The 204 branch disabled: killed by P1_P2, P5 and P13.
  - A deny over the link ignored: killed by P5 alone.
  - No stop on a new pair-start: killed by P8.
- **Bug, open:** `TestPendingClient_F8_CancelEndsPromptlyAnywhere` fails at
  `internal/fleetlink/pending_client_test.go` ~366 ("goroutines 5 > base 4").
  The orchestrator reproduced it with `go test -count=1 -run F8_Cancel
  ./internal/fleetlink/`. It passed in the reviewer's worktree. The test closes
  its httptest servers only in `t.Cleanup`, after `settleGoroutines`. Fix: close
  each env before the settle.
- **Not verified:** a real two-binary run, approve → joiner leaving `waiting`
  within 2 s. No harness boots a TLS main till with enrolment, so this is still owed
  as a manual check.

## Gates run
- gofmt, `go build ./...`, `go vet` on fleetlink and pages, and golangci-lint (0 issues)
  passed, run by Dev and the reviewer.
- `go test -race ./internal/fleetlink/` passed in the reviewer's worktree but fails on F8
  in the shared checkout.
- `go test -race -run PendingJoin ./internal/pages/` passed.
- `TestNoUnrecoveredGoroutines` passed.
- shellcheck on `scripts/ci/*.sh` and `guard-shellcheck-version.sh` passed (orchestrator).
- The ci.yml build-job guards passed (Dev).
- A full `go test ./...` must be re-run after the fixes.

## Required before merge
Findings 1–3, the F8 test fix, then the full gate and CI. Then merge the ut-docs
PR (ADR-0114 status lines, `architecture/lan-sync.md`) together with this one.

Pre-flight: web/help topic updated (en/de/tr/fa/ar multitill step 3; help
image manifest hashes refreshed, no PNG change, `make docs-shots` not run: no
rendered change); reference/ADR updated (ADR-0114 item 10 + Rollout status,
architecture/lan-sync.md, ut-docs branch docs/5330-pending-link-joiner);
en.json keys n/a (none added); README n/a (no claim changed); language packs
n/a (no keys).
