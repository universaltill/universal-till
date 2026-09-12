# Review: /open-orders' "Back to sale" link is mode-aware (ut-docs#2146)

## What shipped

`/open-orders`'s "Back to sale" link plain-linked to bare `"/"`, which
re-applies whatever `display.mode` landing preference is current rather than
necessarily showing the sale screen:

- On a `backoffice`-mode till, a manager hitting `/` is bounced to
  `/backoffice` (`registerIndex`'s own comment already documents that
  redirect as "a default landing preference, not a role bypass" — a
  non-manager session on the same till already falls through to the sale
  screen). An operator who just tapped an explicit "Back to sale" link has
  opted out of that default the same way.
- On a `self_order`-mode till, `/` redirects *every* authenticated session
  to `/self-order` — that one is ADR-0020 kiosk containment, deliberately
  unconditional, not a preference.

Fix: a new shared helper, `saleScreenReturnURL(mode)` (`internal/pages/
index_page.go`), decides the link target — `"/?stay=1"` for `backoffice`
(the `stay=1` param is a new, explicit bypass `registerIndex`'s own
backoffice-redirect branch checks for), `"/self-order"` for `self_order`
(unchanged behavior, just made explicit and shared), `"/"` otherwise.
`open_orders_page.go`'s `GET /open-orders` handler computes it and passes
it into the template as `.backToSaleURL`; `open_orders.html`'s link now
reads that field instead of a hardcoded `/`.

## Dependency note (BA finding, not a defect)

The originating card's headline problem statement described a
`POST /open-orders/resume` redirect — that route does not exist on `main`
yet; it ships in `universal-till#1103` (open, unmerged, a different lane's
card, ut-docs#2138). This fix instead covers the part of the card's own
acceptance criteria that IS live on `main` today (the page's pre-existing
"Back to sale" link) and leaves `saleScreenReturnURL` as a ready-to-reuse
helper for #1103's redirect once it lands — not touched here, since that
PR belongs to a different lane and predates this fix.

## Independent review

Fresh-context Sonnet subagent, isolated worktree (`complexity:easy`
routing).

**Process note, for anyone reading this record later:** the review agent's
isolated worktree started clean (identical to `origin/main`) because this
diff was still uncommitted in the primary checkout when the agent was
launched, and isolation means the agent cannot see the primary checkout's
git state. The agent correctly noticed the mismatch, reconstructed the diff
by reading the primary checkout's files directly, and reproduced it
file-by-file in its own worktree to build/test against. Its first read of
`web/ui/pages/open_orders.html`, taken while this session was mid-shuffle
resolving concurrent-lane merge conflicts on two *unrelated* PRs, caught
the file in a transient state — briefly `git checkout --`-reverted to
un-fix it while its contents were preserved elsewhere pending a branch
move — and reported the fix as absent (**this review's first-round
"Critical" finding**). Re-verified immediately after: the real, current,
committed-and-pushed state of `open_orders.html` on
`fix/2146-open-orders-mode-aware-back-to-sale` (commit `cf7e181`) already
reads `href="{{ .backToSaleURL }}"`, and all 5 new tests pass against it
(`go test ./internal/pages/ -run 'TestOpenOrdersBackToSaleURL|
TestSaleScreenReturnURL|TestStayParamBypassesBackofficeRedirect' -v` —
verified again just now, PASS on all five). That finding does not apply to
the code actually being merged. Lesson for next time: don't launch an
isolated-worktree review against an uncommitted diff in a primary checkout
that's still being concurrently mutated for other work — commit to a
branch first.

**Findings that DO stand, verified independently:**

1. **Scope gap (real, acted on):** `web/ui/pages/menu.html:7` and
   `web/ui/pages/error_page.html:13` (via `internal/httpx/render_error.go`'s
   `RenderError`) both hardcode the identical `href="/"` for the same
   `menu.back_to_sale` link, with the identical latent issue — verified by
   direct read of both files. Filed as ut-docs#2154 rather than folded into
   this card: the acceptance criteria here were scoped to `/open-orders`,
   and `saleScreenReturnURL` already exists as a directly reusable helper
   for that follow-up.
2. **`stay=1` safety (verified, no issue):** `"/"` is not auth-exempt, and
   the only anonymous carve-out for it (`auth.Middleware`'s
   `anonymousRootDest`, wired in `internal/pages/init.go`) fires only for
   `self_order` mode, never `backoffice` — an anonymous request to a
   backoffice-mode till's `/` (with or without `stay=1`) is still sent to
   `/login` before `registerIndex`'s handler runs. So `stay=1` is reachable
   only by an already-authenticated session, for which it does exactly
   what a non-manager session on the same till already does unconditionally
   (fall through to the sale screen) — no privilege bypass.
3. **`self_order` handling (verified, no issue):** never returns the
   cashier screen, consistent with ADR-0020.
4. **`saleScreenReturnURL` switch exhaustiveness (verified, no issue):**
   `display.mode` only ever persists as `""`, `"backoffice"`, or
   `"self_order"` (`POST /api/settings/display-mode` collapses `"register"`
   to `""` before writing) — the `default` branch correctly covers both.
5. **XSS/escaping:** moot — `backToSaleURL` only ever takes one of three
   fixed Go-side literals, no user input reaches it.
6. **Test fixture split reasoning (verified correct):** the two different
   test mux helpers (`newOpenOrdersTestMux`'s minimal held-sales schema vs.
   the full `seedForPages` fixture) are each used for exactly the tests that
   need what they provide (the full fixture is needed wherever
   `registerIndex` itself runs, since it also queries payment methods).

## Verified beyond automated tests

- `gofmt -l .`, `go build ./...`, `go vet ./...`, `golangci-lint run
  ./...` — all clean.
- `go test ./...` — full suite green (run twice: once before the final
  rebase onto `main` post-#1098/#1107 merges, once after).
- CI-blocking guards: `guard-i18n.sh`, `guard-data-access.sh`,
  `guard-help-topics.sh`, `guard-help-drift.sh`, `guard-page-http-error.sh`,
  `guard-kiosk-engine.sh`, `guard-compliance-claims.sh` — all green. No new
  locale keys (no new user-facing strings), so `guard-i18n.sh` and
  `lang-pack-drift` are unaffected.
- TDD verified for real (this session, and independently re-verified by
  the review subagent in its own worktree): reverting `index_page.go`/
  `open_orders_page.go` (keeping the new test file) fails the package to
  **build** (`undefined: saleScreenReturnURL`); restoring returns to green.

## Verdict

Safe to merge. The one real defect the review surfaced was a false
positive from reviewing an in-flux checkout rather than the actual
committed diff (see process note above) — re-verified against the real
commit, all 5 new tests plus the full existing suite pass. The one
legitimate scope-gap finding is filed as a follow-up (ut-docs#2154), not a
blocker for this card's own acceptance criteria.
