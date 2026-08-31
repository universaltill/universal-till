# Code review — products panel has no visible scroll affordance

- **Date:** 2026-08-31
- **Ticket:** ut-docs#1313
- **Branch:** `fix/1313-products-scroll-affordance`
- **Reviewed commit:** `d4cccb78` ("WIP: pre-review snapshot for ut-docs#1313")
- **Reviewer:** independent pass, fresh-context Sonnet subagent (never saw the
  dev reasoning), run in an isolated `git worktree` (per the `reviewer`
  skill's revert-then-restore rule, ut-docs#386). Complexity: `easy`, so per
  "Model routing by complexity" this is the one tier where "different model"
  relaxes to "different instance" — Sonnet built it, a clean-context Sonnet
  reviewed it.
- **Verdict: SAFE TO MERGE**, after fixing the one blocking finding (below).

## What shipped

CSS-only behaviour change plus its test:

- `web/public/app.css`: `.pos-container > .products` gets a pure-CSS "scroll
  shadow" — four layered `background-image` gradients (two solid-to-
  transparent "cover" layers, two `radial-gradient` "shadow" layers), split
  `background-attachment: local, local, scroll, scroll`. The local-attached
  cover only uncovers the scroll-attached shadow once there's real
  scrolled-past distance, so the cue self-hides at whichever edge has been
  fully seen and shows at whichever edge still has unseen content — no JS,
  no scroll listener, no IntersectionObserver. Visible on first paint,
  identical in kiosk and windowed mode (both already give this panel
  `overflow-y: auto`), no hover dependency.
- New spec `e2e/tests/products-scroll-affordance-1313.spec.ts` (1 test, run
  at a deliberately short 1024x480 viewport to force real overflow of the
  demo-seeded catalog): asserts genuine overflow exists, asserts the
  computed `background-attachment`/`background-image` show the exact 4-layer
  local/local/scroll/scroll wiring, and asserts the panel is still a
  functionally working scroll container (regression check for "no
  regression to touch/drag scroll" AC).
- `web/help/img/manifest.json` + all 92 regenerated manual screenshots
  (`make docs-shots`) — see "What I changed" below.

No Go code is touched — confirmed, the diff contains zero `.go` files.

## TDD claim: independently re-verified (by the review subagent, in its own isolated worktree)

Reverted only `web/public/app.css` to `main`'s version, kept the new spec,
re-ran it:

```
✘ 1 failed (895ms)
Error: expected 4 background layers split local/local/scroll/scroll
  - Expected: ["local","local","scroll","scroll"]
  - Received: ["scroll"]   (the pre-existing single-layer `background: var(--surface)` rule)
```

Real, on-topic failure — not a timeout/crash. Restoring the CSS returns it to
green: **1 passed (6.4s)**. Confirmed again in my own follow-up run after the
docs-shots fix (below): still red-then-green, same shape.

## Independent measurement (review subagent) + follow-up verification (mine)

Review subagent ran, from a clean worktree at `main`'s tip:

| Check | Result |
|---|---|
| `gofmt -l .` / `go build ./...` | empty / pass (no Go files touched) |
| New spec (`products-scroll-affordance-1313.spec.ts`) | 1/1 pass |
| TDD revert-then-restore | fails red with the exact expected error, passes restored |
| `guard-i18n.sh` / `guard-compliance-claims.sh` | pass / pass |
| `sale-screen-213.spec.ts` + `sale-screen-category-tabs-search-418.spec.ts` | 10/10 pass, no regression |
| **`guard-docs-shots.sh`** | **FAILED** on the diff as committed (passed on `main` baseline) — blocking |

## What I changed after review

**Fix 1 (blocking) — `guard-docs-shots.sh`.** The guard hashes every file
under `web/public/**` (any `app.css` change is "exactly as visible in a
screenshot as a template change," per the guard's own comment) into the
manual's freshness manifest — so this CSS-only diff invalidated it even
though no screenshot visibly differs. Ran `make docs-shots` for real (92
Playwright specs, both the default and auth tills, all four shipped
locales): **92/92 passed**, regenerated `web/help/img/manifest.json` (surface
hash `8dc3ca855999…`) and all 92 PNGs. Re-ran the guard: now passes. This
also answers the "does the manual need updating" question from the
`reviewer` skill's checklist: no prose/step change is needed (a passive
scroll shadow doesn't alter any documented step), but the mechanical
screenshot-freshness requirement still applies and is now satisfied.

**Fix 2 (non-blocking, addressed anyway) — hardcoded transparent color
coupled to `--surface`.** The two `linear-gradient(...)` "cover" layers use
`rgba(255, 255, 255, 0)` as "transparent." Today that's harmless — `--surface:
#ffffff` is the only definition in the file, no dark/theme override exists —
but it's silently coupled to that current value: once a theme plugin (e.g.
`ut-plugin-theme-midnight`, named as a first-class example in
`adr/0009-plugin-repo-naming.md`, with a live fixture already in
`internal/data/plugin_repo_test.go`) overrides `--surface` to something dark,
these gradients would still fade toward *white*-transparent, producing a
visible white halo at the scroll edges — worse than no cue at all. No CI
guard catches this (no CSS-token-linting guard exists). Rather than switch to
`color-mix(in srgb, var(--surface), transparent 100%)` untested against the
kiosk's pinned `webkit2gtk-4.1` (ADR-0028) with no dark theme to verify
against yet, I added an explicit comment flagging the coupling and the exact
fix for whoever ships the first dark theme — the safer choice for an
`easy`-tier card whose scope is the scroll cue itself, not a theming
overhaul.

**Re-ran the full gate after both fixes:** `gofmt -l .` (empty), `go build
./...` (pass), `guard-data-access.sh` / `guard-kiosk-engine.sh` /
`guard-i18n.sh` / `guard-compliance-claims.sh` / `guard-help-topics.sh` /
`guard-docs-shots.sh` (all pass), and re-ran the new spec plus the two
neighboring sale-screen specs against a fresh server: **11/11 pass**. Full
`go test ./...` (unaffected by a CSS-only change, but run anyway since it
was already in flight): all packages pass.

## Non-issues checked and confirmed clean (review subagent)

- No hover dependency (grepped the new block: no `:hover`).
- No selector/specificity collision with other rules touching `.products` —
  the two responsive `max-height` overrides (900px/480px tiers) only touch
  `max-height`, never `background`.
- No real client/shop name, no secret-shaped literal.
- The two recurring bug classes this pipeline watches for (missing
  `os.MkdirAll`, a cwd-relative path instead of `paths.Data(...)`) don't
  apply — there's no file-write handler or path logic in this diff.

## Deferred (not this card's scope)

- Applying the same two-gradient-pair scroll-shadow recipe to `.basket`/
  `.tender` — they have their own pinned-footer/tab-panel scroll patterns
  already and weren't asked for by this ticket.
- Swapping to `color-mix()` for the transparent color — deferred until a
  dark/theme-plugin surface actually exists to verify against (comment left
  in place naming the exact fix).
