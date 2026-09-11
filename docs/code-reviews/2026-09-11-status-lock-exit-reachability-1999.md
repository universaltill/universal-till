# Code review — status/lock/exit reachability rule, till-side (ut-docs#1999)

- **Date**: 2026-09-11
- **Card**: universaltill/ut-docs#1999
- **Lane**: `lane:local`
- **Reviewer**: independent fresh-context subagent, Sonnet (`complexity:easy`)
- **Companion record**: `ut-docs/code-reviews/2026-09-11-status-lock-exit-reachability-1999.md` — the canonical one; the rule itself is written there.

## What this repo's half of the change is

Docs and comments only — no behaviour change.

The product owner decided ut-docs#1999 on 2026-09-11, and **inverted** the question the card had asked. The card proposed excusing full-screen *admin* surfaces from this repo's own "Status/lock/exit must always be reachable" rule. The decision instead makes that rule **universal**, admin surfaces included, with **self-order kiosk mode** as the single exception — and there the requirement runs the opposite way: status, lock and exit-to-OS must be deliberately *unreachable*, because the device is in front of a member of the public who should be able to do exactly one thing. One axis: the device's mode, never admin-vs-sale.

- `CLAUDE.md` — the Offline-first sentence was a run-on that read as if the sale flow were the scope. It is now its own explicit, unscoped bullet naming the kiosk exception and the compact-affordance requirement, pointing at `ut-docs/reference/coding-standards.md` §10 for the full text.
- The two source comments the reviewer flagged are **deliberately not in this PR** — see "Why the comment fix is not here" below.

## Finding acted on in this repo

**CONFIRMED, medium — the source comments claimed ut-docs#1999 was fully settled.**

Both files carried a comment saying Escape-to-close settles ut-docs#1999 "for every dialog on this pattern". That was true when written, and became false the moment the decision added a second requirement to the same card. These two comments are what an engineer actually reads while building a dialog, so the wrong claim sat closer to the work than the docs did.

Failure scenario, concretely: someone adopting this pattern on a new screen greps `ut-docs#1999`, reads "settling", concludes reachability is handled by the shared dialog JS, and ships another full-screen surface that covers the nav rail.

The fix was written, then deliberately backed out of this PR. See below.

## Why the comment fix is not here

Both files sit inside `guard-docs-shots.sh`'s hashed surface (`web/ui/**`, `web/public/**`), and that guard hashes **whole files**, comments included. Editing two comment lines failed the `build` job with:

> guard-docs-shots: the app surface (web/ui/**, web/public/**, or internal/pages/**.go) changed since the manual's screenshots were last taken

The only way through is `make docs-shots` — regenerating all 104 manual screenshots plus the manifest — for a change that cannot alter a single pixel. That regeneration also conflicts with every other open PR in this repo by construction (the whole artifact set rewrites), and four are open right now.

This is a known, documented false-positive class: the guard's own header records three prior instances (ut-docs#620 unscreenshotted routes, #659 `.DS_Store`, #1351 `testdata/` fixtures), each fixed by narrowing the fileset. A comment-only edit is the same class and is not yet excluded.

So the comment correction moves to **ut-docs#2099**, which implements the affordance and therefore edits both of these files *and* genuinely changes pixels — it pays for the screenshot regeneration for a real reason, and the comments land correct at the same moment the claim they make becomes true. Added to that card's acceptance criteria rather than left to memory.

What stays in this PR is `CLAUDE.md` and this record, neither of which is inside the guarded surface.

## The live violation this records

`.record-dialog { position: fixed; inset: 0; z-index: 500 }` (`web/public/app.css:3434`) sits above `.nav`, which has no `z-index`. `nav.html` and `session_chip.html` are where the lock button and the session/sync/fiscal chips live. So every full-screen record dialog — `/categories`, the reference implementation, included — currently eclipses all three, visually and for pointer events. Under the rule as just decided that is a **live** violation, not a latent one. Tracked as **ut-docs#2099**; it gates ut-docs#2092 and ut-docs#2012.

## Verification

`go build ./...` clean; `go vet ./internal/pages/` clean; `go test ./internal/pages/ -run 'Categor|RecordDialog|Dialog' -count=1` passes — run while the comment edits were still in the tree, which is what proved the `{{/* */}}` template comment kept the template set parsing. As shipped this PR touches no Go, template or asset file at all.
