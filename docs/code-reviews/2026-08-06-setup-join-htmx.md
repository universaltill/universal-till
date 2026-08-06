# Code review — first-boot "join an existing shop" was silently dead

**Date:** 2026-08-06
**Card:** ut-docs#344 (raised to `p1` on a field report, `complexity:medium`)
**Branch:** `fix/344-setup-htmx`
**Reviewer:** independent subagent, Opus, fresh context (no sight of the
implementation reasoning).

## The report

A shop owner reported that a Raspberry Pi 5 could not join an existing shop
hosted on a Pi 4 — the **Join** button appeared to do nothing.

## Field verification (before writing any code)

SSH'd to both devices rather than reasoning from the template alone:

- The joining till is genuinely at **first boot** (`/` → `/login` → `/setup`),
  so the wizard's join step is the correct path for it, not an avoidable one.
- The served page: `hx-post` × 1, `htmx.min.js` × **0**, `alpine.min.js` × 1.
- Posting directly to the endpoint, bypassing the browser, returned a
  correctly-formed localized fragment — `502` with
  `<span class="muted">✗ paste the full code shown on the other till</span>`.

That last point mattered: it established the server side was entirely healthy
and the defect was purely client-side, which a template-only reading could not
have shown.

## Root cause

`web/ui/pages/setup.html` is a standalone document. It carried
`hx-post="/api/setup/join"` with `hx-target`/`hx-swap`, but loaded only
`alpine.min.js` and `cursor.js`. Without htmx those attributes are inert
markup, and the form has no `action`/`method` fallback — so submitting issued a
plain GET back to `/setup`. No request ever reached the handler; the live
region never filled. Multi-till enrolment (ADR-0011 D2) was impossible on a
fresh install.

## What the independent review found — the second bug

The review's blocker is the reason this record exists, because the original fix
was *insufficient in exactly the case the user was most likely to hit*.

**htmx 1.9.12 discards the response body on a non-2xx status.** Verified in the
vendored copy:

```js
var i = f.status>=200 && f.status<400 && f.status!==204;   // shouldSwap
```

Every failure mode of `POST /api/setup/join` answers **502** — unreachable
primary, expired or reused code, malformed paste, snapshot download failure —
and `internal/pages/sync_api_test.go` asserts that 502. So with htmx merely
loaded, the entire error path *still* rendered nothing: the operator would see
the same blank result as before the fix, for an unreachable till or a
mistyped code. Only the happy path became observable.

The codebase already knew this — `web/public/app.js` and
`web/ui/pages/self_order_shop.html` both carry `htmx:beforeSwap` workarounds —
but both are scoped to their own path prefixes *and* to 4xx, so neither covers a
502 from here, and `setup.html` loads neither.

Fixed with a `htmx:beforeSwap` listener scoped to `/api/setup/join` and
`status >= 400`.

### Other review findings, all addressed

- **The guard missed real htmx usage.** Its `hx-*` list was an allowlist of 14
  names; the repo already uses `hx-on` 23×, `hx-vals` 19×, plus `hx-push-url`,
  `hx-params`, `hx-encoding`, `hx-swap-oob`. Proven with a page whose only
  usage was `hx-on` — it passed. Now matches `(data-)?hx-[a-z-]+=` generically.
- **The guard passed a commented-out script tag.** `grep htmx.min.js` matched
  inside `<!-- -->`. My first attempt at this fix *also* failed, for the same
  reason — the tag-shaped regex still matches within a comment. Now every check
  runs against the file with HTML comments stripped.
- **The guard false-positived on partials mentioning `<html>` in a comment**,
  failing CI with no available fix (you cannot add htmx to a partial). Now
  anchored on `<!DOCTYPE`/`^<html`.
- **The guard could not be tested.** It ignored its arguments and always
  scanned the repo, so my first three "adversarial" checks silently tested
  nothing. It now accepts explicit fixture paths — the reason the three holes
  above are demonstrable at all.
- **The render test could go vacuous** (`strings.Contains(body, "htmx.min.js")`
  matches this file's own comment). Now asserts a real `<script src=…>` via
  regexp, plus the presence of the `beforeSwap` handler.
- **`POST /api/setup/join` had no handler test at all.** It is
  middleware-exempt, so its `NeedsFirstBoot` gate is the only thing stopping an
  unauthenticated stranger re-enrolling a configured till and overwriting its
  database. Now covered, and mutation-tested: removing the gate makes it fail.
- **No progress feedback on a request that can block ~60s.** `completeJoin`
  allows a 60s timeout then downloads a full DB snapshot. With an inert button
  the operator presses again, burning the single-use token — the retry then
  fails with "code used or expired?", which (per the blocker) rendered as
  nothing. Added `hx-indicator` + `hx-disabled-elt`.
- **The manual contradicted the product.** `web/help/*/multitill.md` said
  "Enter the main till's address (shown in its Settings)". The form wants a
  *pairing code* — a JSON blob carrying a one-time token; an address alone
  fails. Rewritten in **all four locales** (en/fa/ar/tr), including the
  don't-press-twice warning and what the common failures are.
- **Reflected markup.** `err.Error()` embeds the operator-pasted URL and was
  written unescaped. Now `html.EscapeString`, on both join endpoints.

### Found while fixing the manual — filed separately

Adding `/setup` to `multitill.md`'s `routes:` (already owned by `users.md`)
**silently disabled the entire help-topic registry** — every contextual help
link across the product degraded to the generic `/help` index, including
`/catalog`, `/reports`, `/users` and `/`. One duplicate line in one topic takes
out contextual help everywhere, with no loud failure. Also, the
`scripts/ci/guard-help-topics.sh` that `CLAUDE.md` says enforces this **does
not exist**. Filed as **ut-docs#361**; the offending route was reverted.

Also filed: **ut-docs#357** (unrelated — ut-infra zitadel root).

Deferred, not silently dropped: the join's failure strings are hardcoded
English (`err.Error()`) while the success half is translated — on the one
screen where the operator has *just* chosen their language. `guard-i18n.sh`
cannot reach `err.Error()` values. Pre-existing and out of this diff's scope.

## Verification

The point of this section is that **both** defects are proven by a test that
fails without the fix — in a real browser, since neither is visible to a Go
render test (one needs a browser to execute the attribute, the other to perform
the swap).

New e2e test in `e2e/tests/login.spec.ts` drives the real form on the real
first-boot server with a deliberately bad code, and asserts the operator is
actually told what went wrong:

| state | result |
|---|---|
| both fixes present | **pass** |
| `htmx:beforeSwap` handler removed | **fail** |
| `htmx.min.js` script tag removed | **fail** |

It runs in its own browser context, deliberately: the 502 emits a console
error, and the shared `watchConsole` assertion is checked by every later test in
that serial describe. Isolating it keeps that guard strict for everyone else
rather than exempting "502" globally — which the first draft got wrong and CI
caught.

Full gate, all green:

- `go build ./...`
- `go test ./... -race` — all packages pass
- `npx playwright test` — **57/57**
- guards: data-access, i18n (829 keys, all locales match), htmx-loaded,
  kiosk-launch-flags, emoji-font
- Guard re-broken after hardening with a fresh fixture combining every trick at
  once (comment on `jobs:`-equivalent, quoted key, odd indent, prose
  mitigations inside a `run:` block, commented-out tag): all caught.

### Live on the reporting hardware

Cross-compiled `linux/arm64`, deployed to the joining till, service and kiosk
restarted. Verified on the device:

```
GET /setup -> HTTP 200
hx-post: 1   htmx.min.js: 1   beforeSwap: 1   hx-indicator: 1
/public/vendor/htmx.min.js -> HTTP 200, 48101 bytes
POST /api/setup/join (bad code) -> 502 + its ✗ fragment
```

Previous binary retained as `unitill-pos.bak-fix344`.

### Not yet verified

**Nobody has completed a successful join between two tills.** The failure path
is proven end to end in a real browser and on the device; the *success* path
(valid code → 200 → shop copied across) is exercised at the server level by
`sync_api_test.go`'s two-server join for the sibling endpoint, which shares
`joinPrimary`, but has not been driven through this screen. The device is
deployed and staged for the shop owner to do exactly that.
