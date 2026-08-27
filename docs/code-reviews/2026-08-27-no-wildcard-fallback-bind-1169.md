# Code review — never bind wildcard on a fallback port (ut-docs#1169)

- **Date**: 2026-08-27
- **Card**: ut-docs#1169 (p1, `complexity:hard`, `security`, `source:user`)
- **Branch**: `fix/1169-no-wildcard-fallback-bind`
- **Review**: independent subagent, Opus, in its own git worktree, briefed
  with the diff scope and told to actually run build/vet/tests/guards, not
  just read the diff. One round.

## What shipped

`internal/server/server.go`'s `listenWithFallback` binds the configured
`UT_LISTEN_ADDR` (default `:8080`, an intentional wildcard/all-interfaces
bind — self-order kiosk clients and till-to-till LAN pairing legitimately
need `:8080` reachable off-device), or the next free port when that fails.
Before this change, the fallback repeated whatever host the original address
had — including wildcard. On a real Pi 5 desktop-kiosk-overlay install this
turned a boot-order race between `unitill-pos.service` (systemd) and
`unitill-desktop`'s embedded child into a live incident: whichever process
lost the race for `:8080` fell back to `*:8081` — an empty, never-configured
till, unauthenticated up to login, reachable from the entire LAN, and
discoverable/pairable from another till's setup-wizard pairing screen
(ut-docs#1169's own comments, 2026-08-27).

The fix: a new `isWildcardHost` check. When the *originally configured* host
was wildcard, every fallback bind (the +1..+20 port sweep, and the final
port-0 last resort) uses `127.0.0.1` instead — still reachable for local
diagnosis, never off-device. A non-wildcard configured host (e.g. an
explicit loopback dev override) is unchanged: fallback keeps that same host,
exactly as before.

**Explicit scope carve-out, stated up front and confirmed by the review**:
this closes the *security* half of #1169 (the LAN exposure) only. It does
NOT fix the underlying boot race (`cmd/unitill-desktop/desktop.go`'s
`tillAlreadyRunning` attach-check is a point-in-time probe, not immune to a
slow-starting systemd service), and does NOT reconcile data on devices that
are already split. Both are split into ut-docs#1187 — deliberately, because
the race-fix lives in code carrying `//go:build desktop` (CGO + GTK/WebKit),
which this sandboxed pipeline environment cannot build or test
(`pkg-config --exists gtk+-3.0 webkit2gtk-4.1` fails here), matching the
same documented limitation as
`docs/code-reviews/2026-08-25-attach-mode-window-control-1039.md`.

## Independent review findings — two medium, three low; all triaged this round

**M1 — `isWildcardHost`'s string-literal switch missed real leaks.**
Covering only `""`/`"0.0.0.0"`/`"::"` still let a fallback bind wildcard for
`"::0"`, `"0:0:0:0:0:0:0:0"`, and `"::ffff:0.0.0.0"` — all of which
`net.Listen` also treats as all-interfaces (proved by the reviewer with a
direct probe: `0.0.0.0:PORT` bound for all three). *Fixed*: replaced the
switch with `net.ParseIP(host).IsUnspecified()` (plus the empty-string
case), which can't drift out of sync with what `net.Listen` itself accepts
as wildcard. New table-driven test
(`TestListenWithFallback_WildcardHostFallsBackToLoopback`) covers all six
spellings; confirmed each now falls back to `127.0.0.1`.

**M2 — the mDNS advertiser still broadcasts a degraded instance as a
pairable primary, at the wrong port.** `internal/app/app.go` constructs
`discoveryAdvertiser` with `listenPort(cfg.ListenAddr)` *before*
`server.Start` resolves the actual bound address — `cfg.ListenAddr` is only
rewritten inside `Start`. So even after this fix, a fallback (now
loopback-only) instance still advertises itself on the device's real LAN
IPs at the originally-configured port, because `RoleCheckFromSettings`
returns "primary" for any till with no `sync.primary_url` set — exactly the
never-configured phantom this bug report is about. The practical effect is
strictly safer than before (nothing is actually reachable at the advertised
address, vs. previously being reachable-but-wrong), but it does not satisfy
ut-docs#1179's expectation that "the phantom entry should no longer appear
at all" once #1169 lands. **Deferred to ut-docs#1187** rather than fixed
inline: the correct fix reorders `app.Run` to bind before starting the
advertiser and threads a pre-bound listener into `server.Start`, and
`internal/app/app_test.go` has an existing test that explicitly depends on
today's ordering ("pages.Init has already run by the time server.Start's
bind fails") — this needs its own deliberate change, not a drive-by inside
an already-scoped security fix. Recorded on #1187 with the concrete fix
shape.

**L1 — the degrade is silent to the operator.** Only a `log.Printf`; when
the *systemd service* (not the desktop child, which was already
loopback-only by construction) loses the race, self-order kiosk clients and
till-to-till pairing silently stop working with no operator-visible cause.
CLAUDE.md's own offline-first rule calls for a status chip/banner rather
than a modal, not silence. Deferred — noted on ut-docs#1187 rather than
blocking this fix; fail-closed (loopback-only) is the correct default
either way.

**L2 — `TestListenWithFallback_NonWildcardHostKeepsHost` was a
near-duplicate of `TestListenWithFallback_BusyPort` with no discriminating
power** (same setup, passed both before and after the fix). *Fixed*: folded
its host assertion into `BusyPort` itself and removed the separate test.

**L3 — process/docs.** This review record (you're reading it) closes the
missing-review-record half. `README.md`'s `UT_LISTEN_ADDR` documentation
didn't mention the new degrade-to-loopback fallback behavior — a real
user-visible operational change. *Fixed*: added a note.

## What was verified rather than taken on trust

- **TDD claim re-verified independently.** The reviewer reverted only the
  `fallbackHost` lines (keeping the new tests), re-ran
  `TestListenWithFallback_WildcardHostFallsBackToLoopback`, and got a real
  failure: `fallback from a wildcard host bound "0.0.0.0:46254" — want
  loopback-only`. Restored → passes. Re-verified again after the M1 fix
  (table-driven version) the same way, this session: reverting to the
  original three-case switch reproduces exactly the M1 probe's leaked
  cases; restoring the `IsUnspecified` version passes all six.
- **Callers.** `server.Start`'s only use of the returned address is the
  "port busy" log line and `cfg.ListenAddr` reassignment — reviewed as
  unaffected. `openSetupPage(cfg.ListenAddr)` now points at a genuinely
  reachable address instead of a wildcard one, which is a strict
  improvement (previously the local browser-open convenience opened a
  `0.0.0.0:PORT` URL directly).
- **No duplicated wildcard-fallback logic elsewhere.** `cmd/unitill-desktop`'s
  own `freePort`/child-spawn path was checked and is loopback-only by
  construction already (`127.0.0.1:` prefix, `127.0.0.1:0` last resort) —
  no parallel leak there.
- **No conflict with #1097's `DataDirLock`.** That lock is acquired well
  before `server.Start`, so a same-data-dir second instance is already
  refused outright; this fallback path only ever fires for a genuinely
  distinct data dir (this bug's actual scenario) or an unrelated port
  squatter.
- **Full gate, after every fix in this round**: `gofmt -l .` empty,
  `go build ./...` clean, `go test ./... -timeout 15m` — 57 packages, zero
  failures, and all 18 CI-blocking guard scripts in `ci.yml`'s `build` job
  pass.
- **Secrets/PII**: none in the diff — no real shop name, no credential.
- **Scope of touched files**: `internal/server/server.go`,
  `internal/server/listen_test.go`, `README.md`,
  `docs/code-reviews/2026-08-27-no-wildcard-fallback-bind-1169.md`. No
  `web/`/`internal/pages/` touched, so the UX-guidelines/help-manual checks
  don't apply — confirmed, not assumed.

## Verdict

Safe to merge. The security exposure this card exists for — an
unconfigured, un-provisioned till silently reachable and pairable from the
whole LAN — is closed and covered by a failing-first regression test that
was independently re-verified twice (initial fix, then the M1 hardening).
Remaining correctness work (the boot race itself, split-device data
reconciliation, and the mDNS-advertiser residual) is real but requires
either the GTK/CGO toolchain this environment doesn't have, physical
hardware, or a deliberately separate ordering change — tracked on
ut-docs#1187 rather than shipped as a half-verified change bundled into
this one.
