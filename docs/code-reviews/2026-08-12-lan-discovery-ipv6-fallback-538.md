# Code review: LAN till discovery — IPv4-only fallback when the IPv6 mDNS leg can't route

**Card:** universaltill/ut-docs#538 (bug, p1, complexity: medium)
**Branch:** `fix/538-mdns-ipv6-discovery-fallback` (single commit `9093f5c`)
**Date:** 2026-08-12
**Complexity:** medium — Dev via a Sonnet subagent, Review via an independent
Opus subagent (fresh context). One review round; the round found one real,
non-blocking regression introduced by the fix itself, fixed in this same diff
rather than earning a second round. No money/tax/data-loss/security-class
blocker.

## What shipped

`curl http://localhost:8090/api/sync/discover-primaries` on a LAN with no
usable IPv6 multicast route returned `write udp6
[::]:57143->[ff02::fb]:5353: sendto: no route to host` and **zero** results,
while `dns-sd`/`avahi-browse` on the same machine at the same moment found
the peer over IPv4 fine.

Root cause (independently confirmed against the vendored
`github.com/hashicorp/mdns@v1.0.5/client.go` — see below): `client.query()`
calls `sendQuery(m)` **once**, and `sendQuery` writes the v4 leg then the v6
leg of the same probe packet, `return`ing on the first write error.
`query()` then does `if err := c.sendQuery(m); err != nil { return err }`
**before** entering its `for { select { case resp := <-msgCh … case <-finish
} }` receive loop. So on a host where the udp6 socket *binds* fine but
`sendto` has no route, the combined v4+v6 attempt never listens at all — it
isn't that answers are collected and then discarded, the library returns
before a single answer can arrive.

- `internal/discovery/browse.go`: the old `Browse` body is extracted
  verbatim into `browseOnce(ctx, timeout, disableIPv6 bool)`, which now
  returns *both* whatever candidates it collected and whatever error
  `mdnsQuery` returned, independently. `Browse` composes them: run the
  v4+v6 attempt; on `err == nil` return; on error with candidates already
  collected return them anyway; on error with nothing collected and a live
  ctx, retry once with `params.DisableIPv6 = true`; only report an error
  when both attempts come back empty. A genuine "no peers on this LAN"
  (`err == nil`, empty slice) stays distinguishable from "the scan failed".
- `internal/pages/discovery_api.go`: `discoverPrimariesHandler` no longer
  writes `err.Error()` into the response body (ut-docs#303's standing rule).
  The real error goes to `log.Printf("[discovery] LAN scan failed: %v", …)`
  and the client gets a generic `"discovery scan failed"` marker.
- `internal/discovery/browse_test.go` / `internal/pages/discovery_api_test.go`:
  four new browse tests + one handler test.

## Independent review (Opus, fresh context)

Read `browse.go` and `discovery_api.go` in full (not just the hunks), read
the vendored `hashicorp/mdns` `client.go` **and** `server.go`, read both
consumers' inline JS, and personally verified rather than trusted:

- **Root cause is exactly as claimed.** `sendQuery` (client.go) writes
  `ipv4UnicastConn` then `ipv6UnicastConn`, returning the first error;
  `query()` returns that error before the receive loop. Confirmed.
- **`DisableIPv6` genuinely suppresses the failing write**, it isn't just a
  display flag: `Query` → `newClient(!params.DisableIPv4, !params.DisableIPv6)`
  → `use_ipv6 == false` → `ipv6UnicastConn` stays nil → `sendQuery`'s v6
  branch is skipped entirely. The retry is the right mechanism, not a
  cargo-culted flag.
- **The advertise half was never affected** — `mdns`'s `newServer` binds
  udp4/udp6 independently, only errors if *both* fail, and answers on
  whichever socket received the query. So this diff closes the whole
  discovery loop for an IPv4-only LAN; nothing further is needed on the
  responder side.
- **The 64-candidate cap is still global per `Browse` call, not per
  attempt.** The retry is reachable only through the `len(candidates) == 0`
  branch, so exactly one of the two `browseOnce` calls can ever have
  collected anything. No 128, no double-count.
- **The retry cannot double the wall clock in practice.** Every error return
  in the real `mdns.Query` is *pre*-receive-loop (`newClient` bind failure,
  `setInterface`, the first `sendQuery`); the in-loop node-specific re-query
  errors are logged, not returned, and a completed scan returns `nil`. So a
  failing first attempt costs ~0ms and the worst case is
  `discoverBrowseTimeout + ε` ≈ 4s, unchanged. A genuine empty result is
  `err == nil` and short-circuits before the retry. `e2e/tests/tills-lan-discovery.spec.ts`
  budgets 10s against that 4s scan — no new flakiness; if anything the e2e
  gets *greener*, because a sandbox with no IPv6 multicast route previously
  drove the page into its `.catch()` error state.
- **No goroutine leak on the retry path.** `browseOnce` is self-contained:
  its query goroutine `close(entries)`s unconditionally and its `queryErr`
  channel is buffered(1), so neither goroutine can block after the caller
  bails on `ctx.Done()`. The second `browseOnce` gets exactly the same
  treatment as the first. `TestBrowse_DoesNotLeakCollectorGoroutineWhenCancelledMidScan`
  and `TestBrowse_RespectsAlreadyCancelledContext` both still pass (5×,
  `-race`). No data race either: the `candidates` slice only escapes
  `browseOnce` on the path that first waits for `<-collected` and takes
  `mu`; the `ctx.Done()` path returns `nil`.
- **The generic `"discovery scan failed"` body correctly stays outside
  i18n.** Read `web/ui/pages/tills.html` and `web/ui/pages/setup.html`: both
  do `fetch(…).then(r => r.json()).then(…).catch(…)` with **no** `r.ok`
  check and never touch the body text — `http.Error`'s `text/plain` body
  fails `JSON.parse`, the promise rejects, and the operator sees the
  page-local `i18n.error` (`tills.discovery.error`, present in all four
  locales). The string is a machine-facing marker, not user-facing copy.
  `guard-i18n.sh` agrees by design: its Go-side check documents `http.Error`
  as explicitly out of scope, so this passes on policy, not by evading a
  regex.
- **`log.Printf("[discovery] …")` matches house style** (42 existing
  `log.Printf` call sites under `internal/pages`, mostly `[prefix]`-tagged).

### Findings

**R1 — medium, FIXED in this diff. Cancelling during the v4-only retry lost
its context identity.** `Browse` guards `ctx.Err()` *before* the retry
("this is the caller giving up, not a network failure to retry through")
but not *after* it. So a manager closing the Tills tab while the second
attempt is in flight got
`fmt.Errorf("lan scan failed (v4+v6: …; v4-only retry: context canceled)")`
instead of `context.Canceled` — a small behavioural regression the retry
itself introduced, since pre-fix `Browse` always returned `ctx.Err()`
verbatim on a mid-scan cancel, on the exact path the author's own comment
says must not be reshaped. Blast radius today is small (the only caller,
`discoverPrimariesHandler`, doesn't inspect the error): a misleading
`[discovery] LAN scan failed:` line in the operator's log and a wasted 500
write to an already-dead connection. But it silently breaks
`errors.Is(err, context.Canceled)` for any future caller, and the two
existing cancellation tests only cover the *first* attempt's window, so
nothing would have caught it.
Fixed with the symmetric `ctx.Err()` guard after the retry, plus `%v` → `%w`
on both wrapped errors so the underlying failures stay inspectable.
Regression test: `TestBrowse_ReportsCancellationDuringTheV4OnlyRetryAsAContextError`
— confirmed it fails against the as-submitted `browse.go` with exactly this
diagnosis before adding the guard.

Nits noted, not fixed (none change behaviour, none block merge):

- **N2 — an IPv6-only LAN with a broken IPv4 multicast route is still
  unrecovered, and the retry picks the wrong leg for it.** `sendQuery`
  writes v4 *first*, so a v4 write failure aborts before v6 is ever tried —
  and the retry then disables IPv6, the leg that was working. A symmetric
  `DisableIPv4` third attempt would close it. Out of #538's scope (the card
  is about IPv4-only LANs) and vanishingly rare in practice (bind succeeds
  but `sendto` has no route, on IPv4, on a shop LAN), so accepted rather
  than speculatively added; worth a Backlog card if it's ever seen in the
  wild.
- **N3 — the "keep candidates collected before a late error" branch is
  unreachable with the real library.** As traced above, `mdns.Query` can
  only error before it ever sends on `params.Entries`. The branch is
  correct, cheap defence and is honestly described as such in the commit
  message (the commit is explicit that this alone would *not* have fixed
  #538, and `TestBrowse_RetriesIPv4OnlyWhenTheFullQueryFailsOutright`'s
  doc comment says so too) — good discipline, not overclaiming.
- **N4 — the failure path doubles the multicast probes per request.**
  `/api/setup/discover-primaries` needs no session at all; at its 5/min
  rate limit that's at worst 10 tiny PTR probes/min instead of 5. Negligible
  amplification, no new surface.
- **N5 — the UI distinguishes success from failure only by `r.json()`
  rejecting**, with no `r.ok` check and no `{ "error": { code, message } }`
  envelope on the error path (this repo's own documented API convention).
  Fragile-looking, but entirely pre-existing — `main` used `http.Error`
  here too, so this diff changes only *which* plain-text string is
  discarded. Not this card's job; a repo-wide sweep of `http.Error` API
  responses would be its own card.
- **N6 — pre-existing `gofmt` drift** in `internal/pages/external_api_test.go`
  and `internal/pages/import_bkp_page_test.go`. Present at the parent commit,
  untouched by this diff, and there is no `gofmt` gate in CI or in the guard
  scripts. Mentioned only so it isn't mistaken for something this branch
  introduced.

Not applicable, checked anyway: the two recurring bug classes this pipeline
keeps finding — a file-write handler missing `os.MkdirAll`, and a
cwd-relative path where `paths.Data(…)` belongs — genuinely don't apply.
The diff contains no file I/O, no `os.Create`/`WriteFile`/`MkdirAll`, and no
`filepath`/`paths` use at all.

## Verified beyond the automated suite (this session)

- **TDD claim re-verified personally, not taken on faith.** Reverted *only*
  `internal/discovery/browse.go` and `internal/pages/discovery_api.go` to the
  commit's parent and re-ran the new tests:
  - `TestBrowse_RetriesIPv4OnlyWhenTheFullQueryFailsOutright` → FAIL,
    `Browse: unexpected error write udp6 [::]:57143->[ff02::fb]:5353: sendto:
    no route to host — the v4-only retry should have recovered the peer`
    (the card's literal symptom).
  - `TestBrowse_ReturnsCollectedCandidatesDespiteALateQueryError` → FAIL
    (collected candidate discarded).
  - `TestBrowse_ReturnsErrorWhenBothTheFullAndV4OnlyRetryFail` → FAIL
    (`got 1 mdnsQuery calls, want exactly 2`).
  - `TestDiscoverPrimariesAPI_NeverLeaksRawErrorText` → FAIL,
    `response body leaks the raw driver error: write udp6 … sendto: no route
    to host`.
  - `TestBrowse_DoesNotRetryOnAGenuineEmptyResult` passes pre-fix, as
    expected — it's a guard against over-retrying, not a regression test for
    the bug. Correctly written either way.
  Restored both files; all five pass again. The tests are real, they fail
  for the stated reason, and they fail for the *right* reason.
- **Baseline note for the orchestrator:** the `main` ref in this worktree is
  a disjoint history with no merge-base against the branch (its
  `discovery_api.go` still predates ut-docs#289's two-route split), so
  `git diff main` is misleading here. The review was done against
  `9093f5c^` (`6c6e437`), the commit's real parent.
- `go build ./...`, `go vet ./...` — clean.
- `go test ./internal/discovery/... ./internal/pages/... -race -count=1` —
  green; `internal/discovery` additionally run `-race -count=5` to shake the
  timing-sensitive cancellation/goroutine-leak tests. All green, no flakes.
- Full `go test ./... -count=1` — 36 packages ok, 0 FAIL, exit 0. Run twice:
  once against the diff as submitted, once after the R1 fix.
- All five guard scripts green: `guard-data-access.sh`, `guard-i18n.sh`
  (915 template keys, all locales match en.json), `guard-help-topics.sh`,
  `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`.
- **No manual/help-topic work is owed, and this is a deliberate conclusion
  rather than a skip.** The change is backend-only: no route added or
  removed, no template touched, no new control or field. The Tills page and
  the setup wizard still reach exactly the same two terminal states a shop
  owner can see — a rendered result list, or "No primaries found on this
  network." — so no prose, step or screenshot under `web/help/` describes
  anything that changed. What changed is that the *correct* one of those two
  outcomes now appears on an IPv4-only LAN. `guard-help-topics.sh`'s
  page-route coverage check passes unchanged, and `README.md` claims nothing
  affected by this.
- **No real client or shop names, and no secret-shaped literal, anywhere in
  the diff.** Everything is synthetic: RFC1918 test addresses
  (`192.168.1.60/.61`), `till-real` / `till-v4only` / `flood-N` ids, and
  `Real Till` / `V4 Only Till` names. The one address that looks like a real
  capture — `[::]:57143->[ff02::fb]:5353` — is the card's own reported error
  text: a wildcard bind, an ephemeral source port and the IANA mDNS
  multicast group. Nothing identifying, nothing sensitive. The new
  `log.Printf` records a network error, which can contain LAN addresses —
  a local diagnostic, in line with the 42 existing `log.Printf` sites in
  this package, and not a secret.

## Safe-to-merge verdict

**Yes.** The fix is correctly targeted at the real root cause (verified
against the vendored library source, not inferred), it is properly bounded,
it doesn't reintroduce the goroutine leak the previous card fixed, it can't
double the scan's wall clock, and it keeps "no peers found" distinguishable
from "the scan failed" — which is what makes the UI honest on an IPv4-only
LAN. R1 is fixed here with its own regression test; N2–N6 are accepted or
pre-existing and none block merge.

**Uncommitted in the worktree beyond this record** (for the orchestrator to
pull back): `internal/discovery/browse.go` (R1 guard + `%w`) and
`internal/discovery/browse_test.go` (the R1 regression test).
