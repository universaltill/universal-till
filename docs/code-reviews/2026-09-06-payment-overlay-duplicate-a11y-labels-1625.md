# Code review: payment overlay duplicate controls have distinguishing accessible names (ut-docs#1625)

**Branch:** `fix/1625-payment-overlay-duplicate-a11y-labels`
**Card:** universaltill/ut-docs#1625 (found reviewing #1542, complexity:medium)
**Reviewer:** independent Opus subagent, isolated worktree (Sonnet wrote the fix)

## What shipped

`#payment-overlay` opens non-modally (`.show()`, ut-docs#1385 — the custom
on-screen keyboard must stay usable while it's open), so nothing outside it
becomes `inert`/`aria-hidden`. ut-docs#1542 added duplicate Hold Sale / New
Sale buttons inside the overlay so they're clickable even when the overlay
covers the originals in `.tender-default-footer` at narrower desktop
viewports. Because both copies are simultaneously in the DOM and the a11y
tree while the overlay is open, a screen-reader/keyboard user tabbing
through encountered two identically-named "Hold Sale" controls and two
identically-named "New Sale" controls with no way to tell which is which.

Fix: the two in-overlay copies (`payment-overlay-hold`,
`payment-overlay-new-sale`) now carry a distinguishing `aria-label` — two
new locale keys, `tender.overlay_hold_action` / `tender.overlay_new_sale_action`,
added to all four locale files (en/fa/ar/tr). Visible text is unchanged, so
there is no visual/sighted-user change. The originals keep their plain,
unqualified accessible name.

**A blanket `inert` on `.tender-default-footer` while the overlay is open
was considered and rejected** as the fix shape: `new-sale-closes-payment-
overlay-1386.spec.ts`'s "Hold Sale closes an open payment overlay" test
intentionally drives the ORIGINAL Hold Sale button directly at a wide
viewport (1920x1080) where the overlay does not cover it at all — inert-ing
the whole footer whenever the overlay is open would have silently broken
that already-legitimate, already-tested path (and, the independent review
confirmed by actually implementing it, the New Sale half of that same spec
too).

## Independent review — findings

**Verdict: PASS, approved to merge as-is.** No blocking findings. The
reviewer independently re-implemented and ran the rejected `inert` shape to
confirm the rejection was correct (it broke both tests in
`new-sale-closes-payment-overlay-1386.spec.ts`, not just the one cited),
and independently re-verified the TDD claim by reverting each aria-label
one at a time and confirming each assertion goes red on its own (not just
that the first one fails and masks the second).

Two **non-blocker** follow-ups raised, filed as new Backlog cards rather
than folded into this diff (out of scope for a naming fix):

- **ut-docs#1628** — `#hold-modal`'s own submit button is also labelled
  plain "Hold Sale" and opens non-modally, so while the hold dialog is open
  a third "Hold Sale" control joins the two this card already
  disambiguated. Same bug class, pre-existing (predates #1542), reachable
  in the same flow this card's own new spec drives.
- **ut-docs#1629** — the originals stay keyboard-focusable while 100%
  visually covered by the overlay at ≤~1440px (now correctly named, but
  still a WCAG 2.4.11 Focus Not Obscured concern) — `inert` being off the
  table for the reason above means this needs its own, different fix
  shape.

Also confirmed live: `lang-pack-drift` is **already red on `main`**
independent of this branch (a pre-existing orphan key from a different,
still-in-flight core change) — this diff's two new keys add to an
already-red job, not a green one. Handled below via this cycle's own
same-day language-pack follow-up PRs (own change only; the pre-existing
orphan belongs to the other in-flight change).

## Verified beyond automated tests

- TDD: reverted the fix, confirmed both new-spec assertions independently
  fail with the claimed "2 elements matched" error; restored, confirmed
  green.
- Full regression set green: `payment-overlay-footer-reachable-1542.spec.ts`,
  `new-sale-closes-payment-overlay-1386.spec.ts`,
  `payment-overlay-osk-1385.spec.ts`, plus the new spec — 11/11.
- `gofmt -l .`, `go build ./...`, `go vet ./...`,
  `golangci-lint run ./...` (0 issues), full `go test ./...` — all green.
- Guards: `guard-i18n.sh`, `guard-docs-shots.sh` (regenerated via
  `make docs-shots`; only `sell.png` en/ar re-encoded, no visual delta since
  the change is aria-only), `guard-compliance-claims.sh`,
  `guard-help-topics.sh`, `guard-e2e-fixtures-import.sh` — all green.
- Locale wording checked (not just key presence): each locale's aria-label
  carries the button's own visible text verbatim as a prefix (WCAG 2.5.3
  Label in Name) and its parenthetical qualifier is matched to that
  locale's own overlay heading (`tender.title`), not translated literally
  from English.
- RTL (fa/ar): no CSS/layout change, string-only; existing RTL hit-test
  coverage (#1542's own fa spec) still passes.
- No real client/shop name, no secret-shaped literal introduced.
- Manual (`web/help/`): correctly not updated — nothing a sighted operator
  sees or does changed; only the computed accessible name of two controls
  differs.

## Safe-to-merge

Yes. Language-pack follow-ups for the two new keys tracked and opened in
this same cycle (ut-plugin-language-de/es), per CLAUDE.md's lang-pack rule.

## Deferred (new Backlog cards)

- ut-docs#1628 — hold-modal submit button duplicate-name follow-up.
- ut-docs#1629 — obscured-but-focusable originals (WCAG 2.4.11) follow-up.
