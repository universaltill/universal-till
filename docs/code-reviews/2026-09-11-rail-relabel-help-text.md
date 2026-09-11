# Code review: rail relabel help text overstates a visible rename

**Card:** universaltill/ut-docs#2053
**Branch:** `fix/2053-rail-relabel-help-text`
**Complexity:** easy

## What shipped

`web/help/en/menu.md`'s "Tiles a layout plugin has hidden" topic described a
layout plugin's ability to rename the `Inventory`/`Orders` rail buttons as a
plain, always-visible on-screen rename. In fact the rail is icon-only
(`.visually-hidden` labels) at kiosk/tablet widths (>480px, per
`web/public/app.css`) and only renders visible on-screen text at phone width
(<=480px), where the rail becomes a horizontal top bar. An operator reading
the old text, installing a relabeling plugin, and looking at a kiosk/tablet
screen would see no visible change and could reasonably conclude the plugin
was broken.

One sentence was corrected to state the rename reaches the accessible name
(screen-reader text) at kiosk/tablet width, and becomes visible on-screen
text only at phone width. No code change, no new locale key — pure prose
correction in the English base topic. Docs-only diff (`web/help/en/menu.md`,
1 insertion/1 deletion).

## Independent review

Reviewed by a fresh-context Sonnet subagent (complexity:easy → same-model,
different-instance review per `MODEL-ROUTING.md`), isolated in its own git
worktree branched from this branch's pre-review WIP commit.

**Verdict: PASS.** The subagent independently re-derived the CSS breakpoint
claims from `web/public/app.css` directly (rather than trusting the commit
message) and confirmed:
- `.nav-toggle-label` is `.visually-hidden` by default (>480px: kiosk/tablet/
  desktop widths) — icon-only, but present in the accessible name.
- The `@media (max-width: 480px)` block reverts the rail to a horizontal top
  bar and restores visible label text.
- Both claims in the new sentence match the CSS exactly, including the
  480px threshold.

Re-ran all three relevant CI guards independently, all green:
- `guard-help-topics.sh` — no route conflicts, every topic parses, all
  locales complete, page-route coverage intact.
- `guard-help-drift.sh` — no new structural drift for the `menu` topic (all
  reported drift is pre-existing, baselined under ut-docs#1962/#1973, in
  unrelated topics).
- `guard-compliance-claims.sh` — no forbidden fiscal-compliance claims.

Confirmed diff scope is exactly one file, no locale JSON touched (correct:
this is help-manual prose, not a template/JS user-facing string covered by
the `{{ T "key" }}` i18n rule), no secret-shaped literals, no real
client/shop name.

**One low-severity, non-blocking finding, triaged as accepted-as-is:** the
new sentence says "on a kiosk or tablet screen," which is accurate but
slightly narrower framing than the CSS's actual >480px scope (which also
covers desktop widths, per the CSS's own comment) — `catalog.md` elsewhere
in this same manual uses width-based phrasing ("on a tablet-width screen or
wider") for an equivalent breakpoint. Not a factual error (the sentence never
claims desktop shows visible text), so not worth a second review round for
a complexity:easy card per `MODEL-ROUTING.md`'s process-depth guidance —
left as-is.

**Process note, not a content defect:** the review subagent's worktree
initially checked out the wrong commit (a prior unrelated merge, not this
branch's WIP commit) before self-correcting via an explicit `git checkout`.
The review that's actually documented above was verified against the
correct commit (`cad5154`, tip of `fix/2053-rail-relabel-help-text` at
review time). Noted here in case worktree-isolation wiring for review
subagents needs a look.

## What was verified beyond automated tests

- `gofmt -l .` — clean.
- `go build ./...` — succeeds.
- `go test ./...` — full suite green (no Go code touched by this change;
  run anyway per the repo's own "before committing" gate).
- `golangci-lint run ./...` — 0 issues.
- `guard-help-topics.sh`, `guard-help-drift.sh`, `guard-compliance-claims.sh`
  — all green, run independently by both the author and the reviewer.
- Manual cross-check of the corrected prose against the live CSS rules
  governing the rail (`web/public/app.css`), by both the author (before
  writing the fix) and the reviewer (independently, after).

No regression test applies — this is a prose-only correction to static help
content, not a behavior change with a testable pre/post state, so there is
no TDD claim to independently revert-verify.

## CI finding, fixed before merge

The first CI push failed `guard-docs-shots.sh`: the topic-hash portion of
`web/help/img/manifest.json` for `menu`/`en` is computed from
`web/help/en/menu.md`'s own content and hadn't been updated to match the
edited file, even though the actual rendered screen (and its screenshot)
were untouched. Fixed by running `make docs-shots` and committing the
regenerated `manifest.json` (topic hash only — the `menu` screenshot PNGs
themselves are byte-identical, since no template/CSS/Go surface file
changed). The same `make docs-shots` run also produced non-deterministic
re-render diffs in three unrelated PNGs (`ar/sell.png`, `en/catalog.png`,
`fa/catalog.png`) whose manifest hash entries did **not** change — pixel-
level rendering noise between two runs, not a real content drift. Those
were discarded (`git checkout --`) to keep this PR scoped to #2053 and
avoid introducing unrelated, unverified screenshot noise.

## Safe to merge

Yes. Docs-only content change plus the one required manifest-hash update,
zero code/behavior change, all applicable CI guards and the full test
suite green, independent review passed with no blocking findings.

## Deferred / explicitly out of scope

- A `de`/`fa`/`ar`/`tr` equivalent correction to the same paragraph, per the
  issue's own scope note — not blocking, those locales can pick it up
  whenever they next get NAS-translation access.
- The optional "kiosk or tablet" → width-based phrasing polish noted above.
