# Help-topic translation-drift guard, plus the `catalog.md` drift it was written to catch (ut-docs#1962)

**Card:** ut-docs#1962 — Help topics have no translation-parity gate; German's
`catalog.md` describes a UI two releases gone.
**Branch:** `fix/1962-help-topic-drift-guard`
**Complexity:** `medium` → build inline (session model already Sonnet), review
at Opus per the `reviewer` skill's model routing.

## What shipped

1. **A new CI guard, `scripts/ci/guard-help-drift.sh`** (Go half:
   `scripts/ci/checkhelpdrift`), sibling of `guard-help-topics.sh` and wired
   into the same `build` job in `.github/workflows/ci.yml`. Where
   `guard-help-topics.sh` only checks a translated topic *exists*,
   `guard-help-drift.sh` compares it against its English original on
   structural counts — headings, numbered steps, indented (sub-)bullets,
   top-level bullets, and list items with a bold lead-in (`**Term:** …`) —
   language-independent signals that change whenever content is added,
   removed, or restructured.
2. **A baseline/allowlist file**, `scripts/ci/i18n-baseline/help-drift-baseline.json`,
   mirroring the `ut-plugin-language-*`/`i18n-baseline/` convention for
   already-known untranslated keys. Kept outside `web/help/` deliberately —
   that tree is `//go:embed`'d into every shipped binary (`web/embed.go`),
   and this file is CI-only bookkeeping nothing at runtime reads.
   A recorded entry anchors **both** sides of the comparison it's exempting
   — the locale's signature *and* English's, both at record time — and is a
   *live* claim about the current mismatch, not a permanent exemption: the
   guard fails if the real drift no longer matches what's recorded, whether
   because the translation was fixed, because it drifted into a different
   mismatch, or because **English itself moved further since the entry was
   recorded** (see finding 2 below — this last case was missing from the
   first draft and was the review's main blocker).
3. **The originally-reported drift, fixed**: `catalog.md` in `de/tr/fa/ar`
   now matches English's structure exactly. Specifically:
   - **de**: regained the "No photo handy? Choose a built-in image
     instead" and "Tile colour" sub-bullets under step 1 (previously
     missing entirely); regained the "Customization options" Good-to-know
     bullet; step 1's numbered line and the "Artikelbild" sub-bullet were
     rewritten to describe the current full-screen item editor
     (`Artikel hinzufügen` → full-screen editor) instead of the pre-#1901
     side panel (`unterhalb des Editors` → `innerhalb des Editors`).
   - **tr/fa/ar**: regained the "Press Preview or Import once and wait" and
     ".bkp backup carries a product photo" sub-bullets under step 3 (their
     step-1 content and Good-to-know sections already matched English).
   - **de/menu.md**: regained the same-day `## Administration` section that
     `tr/fa/ar` had already picked up from `en/menu.md` (ut-docs#1959) —
     found by the independent review (finding 3), fixed inline rather than
     deferred, since it was one paragraph.
4. **A full sweep found pre-existing drift in 22 other topics** across
   ar/de/fa/tr (not just `catalog.md`) — per the card's own acceptance
   criterion ("a sweep records whether other topics have the same drift"),
   all are recorded in the baseline file with their exact current
   signature (both locale and English sides), rather than fixed inline.
   Filed as a follow-up: **ut-docs#1973**, which flags the largest, most
   clearly content-real gaps (`payments` ar/fa/tr: 3 numbered steps vs
   English's 12; `sell` ar/de/fa/tr: fewer headings/bullets than English;
   `country-settings`/`fiscal-device`/`updates` fa: several numbered steps
   missing) and documents the `-update` flag (below) for working through it.
5. **A `-update` flag** on `checkhelpdrift`, added during review (was
   nitpick 9, promoted once it became the clean way to regenerate the
   baseline after finding 2's schema change): recomputes every
   already-recorded entry's signature/English fields against current
   content, drops an entry outright once it's fixed (no more hand-editing
   JSON to burn down ut-docs#1973) or once its topic/locale no longer
   exists, and deliberately never adds a new entry — picking a `reason` for
   previously-unrecorded drift stays a human/reviewed decision.

Translations (both the `catalog.md`/`menu.md` fixes and this record) were
authored directly in this session, not via the `ut-website` blog's
NAS/Ollama translation pipeline (`ut-docs/reference/translation.md`) —
confirmed against precedent
(`2026-09-09-cash-reconciliation-help-translation-1846.md`,
`2026-09-08-german-user-manual-1827.md`) that direct in-repo help-topic
translation is the established practice for this class of change; that
reference doc's self-hosted-model pipeline is specific to `ut-website`'s
batch blog-post translator, not a requirement for `universal-till` help
content.

## Design notes

- **Counts, not text.** The guard cannot judge translation accuracy — no
  automated check can — but it catches exactly the failure mode that
  motivated this card: a topic gaining or losing whole bullets/sections
  without every locale following along.
- **Baseline entries are per (locale, topic), anchored on both sides** —
  narrow by construction, and a stale one (newly matching, drifted further
  than recorded, or English having moved on) fails loudly rather than
  silently widening what's allowed.
- Reused `internal/manual.Load` for parsing (same pattern as
  `checkhelptopics`) rather than re-parsing front matter — the front-matter
  grammar is exactly the kind of thing that's subtle enough to drift from
  the real implementation if reimplemented.
- **Known, accepted limitations of `computeSignature`** (independent review
  finding 8, not live today — swept the whole 195-file corpus: zero code
  fences, zero tables, zero indented ordered sub-lists, zero `+`-bullets
  across every help topic): fenced code blocks aren't excluded from the
  heading/bullet/step counts; an indented *ordered* sub-list matches
  neither `numberedRe` (anchored at column 0) nor `subBulletRe` (requires
  `-`/`*`), so one going missing in translation would be invisible; and a
  bold-led plain paragraph counts as a "bold lead-in" even though it isn't
  a list item. None of these affect any topic in the manual today, but a
  future topic that introduces one of these shapes could silently blind
  this guard to real drift in that topic — worth knowing before adding a
  code-heavy or table-heavy help topic.

## Verification

- `go build ./...`, `go vet ./...` — clean.
- `go test ./scripts/ci/checkhelpdrift/... -v` — 17 test cases: the
  original 13 (`computeSignature`, `loadBaseline`, `checkAll`'s six
  branches) plus 4 added during review's fix-up: `checkAll` failing when
  English moved since a baseline entry was recorded (the finding-2 fix
  itself), `refreshBaseline` dropping fixed/deleted entries while
  preserving `reason` and never inventing new ones, and `writeBaseline`
  round-tripping with deterministic (locale, topic) ordering.
- `go test ./internal/manual/...` — all 27 existing tests still pass.
- `go test ./...` (full repo suite) — all packages pass, ~7 minutes wall
  clock (`internal/pages` and `internal/plugins` are the long poles).
- `golangci-lint run ./...` (whole repo) — 0 issues.
- `gofmt -l .` (whole repo) — no output.
- `bash scripts/ci/guard-help-topics.sh` — green.
- `bash scripts/ci/guard-help-drift.sh` — green (51 known-drift lines +
  final ✓; was 52 before the `de/menu.md` fix dropped one).
- `bash scripts/ci/guard-i18n.sh` — green (unaffected; no `web/locales/*.json`
  touched).
- `bash scripts/ci/guard-compliance-claims.sh` — green.
- `bash scripts/ci/guard-docs-shots.sh` — green after `make docs-shots`
  regenerated `web/help/img/manifest.json` (see finding 1: this guard was
  red before the fix, since the `catalog.md` prose edits changed topic
  hashes the manifest hadn't caught up to).
- `shellcheck` is not installed in this build environment. The independent
  review compared `guard-help-drift.sh` line-by-line against the
  already-`shellcheck`-clean `guard-help-topics.sh` (identical shebang,
  `set -euo pipefail`, `cd`-to-repo-root pattern; the only differing line
  is which `go run` target it invokes) and judged it will pass. CI's own
  `shellcheck scripts/ci/*.sh` step is the backstop if that's wrong.

## Independent review (Opus, worktree-isolated)

Full pass: read the diff, ran build/vet/test/lint/all guards independently,
did a real TDD-style revert-restore of `de/catalog.md` to confirm the guard
actually catches the original bug, and independently reimplemented
`computeSignature` in Python to spot-check all 52 baseline entries against
the real files. Initial verdict: **not safe to merge** — 2 blockers, 4
real-but-minor findings, 4 nitpicks. Every blocker and real-but-minor
finding was fixed in this branch before merging; every nitpick was either
applied or explicitly deferred as noted.

- **BLOCKER 1 — `guard-docs-shots.sh` would have failed CI.** The
  `catalog.md` prose edits changed topic content hashes that
  `web/help/img/manifest.json` hadn't caught up to (German isn't in the
  manifest at all — a pre-existing gap, not this diff's). **Fixed**: ran
  `make docs-shots` for real (Playwright against the pre-installed
  Chromium), regenerated the manifest and the two screenshots that
  actually changed pixels (`sell` en/ar — unrelated to this diff's own
  topics, plausibly pre-existing nondeterminism in that screenshot's
  content), committed both.
- **BLOCKER 2 — a baselined entry was permanently blind to English-side
  drift.** The first draft's `baselineEntry` recorded only the locale's
  signature; once baselined, English could grow an entire new section and
  the guard would keep passing forever, since only the frozen locale side
  was ever compared. Demonstrated for real by the reviewer (appended a
  fake bullet to `en/sell.md`, a baselined topic — guard still exited 0).
  **Fixed**: `baselineEntry` now also stores `english`, and `checkAll`
  fails when it no longer matches English's current signature (new test:
  `TestCheckAllEnglishMovedSinceBaselineFails`). All 51 remaining entries
  were regenerated with the `-update` flag added for exactly this.
- **Finding 3 (real-but-minor, fixed) — `de/menu.md` missing a same-day
  `## Administration` section** that `tr/fa/ar` already had from
  ut-docs#1959, baselined instead of fixed in the first draft. One
  paragraph — fixed inline; the entry no longer exists in the baseline.
- **Finding 4 (real-but-minor, fixed) — German closing-quote inconsistency**
  in `de/catalog.md`: the new "Anpassungsoptionen" bullet and an adjacent,
  substantively-untouched line used `”` (U+201D, the English closing
  quote) instead of the file's own established `„…“` (U+201E/U+201C)
  convention, disagreeing with the very next line. Fixed both to match the
  rest of the file.
- **Finding 5 (real-but-minor, fixed) — this record itself was inaccurate**
  in its first draft: said "12 other topics" (actually 23 at review time,
  now 22 after finding 3's fix), had an unbalanced backtick, and left the
  full-suite test outcome and this section unfilled. Corrected above.
- **Finding 6 (real-but-minor, fixed) — the baseline file shipped inside
  the product binary.** It lived under `web/help/`, which is
  `//go:embed`'d whole into every POS/desktop/Android build — harmless
  functionally (never read at runtime) but conceptually wrong, and it
  bloats the binary with CI bookkeeping. Moved to
  `scripts/ci/i18n-baseline/help-drift-baseline.json`.
- **Nitpick 7 (accepted, no change)** — `loadBaseline`'s path is
  cwd-relative rather than going through `paths.Data(...)`, but this is
  repo/CI content, not runtime data, and `guard-help-drift.sh`'s own
  `cd "${ROOT_DIR}"` makes it safe in the one context it actually runs.
  Fail-closed (a missing file yields zero baseline entries, turning every
  known drift into a hard failure) rather than fail-open — the reviewer
  confirmed this is the safer default.
- **Nitpick 8 (accepted, documented, no code change)** — see "Design
  notes" above (fenced code blocks, indented ordered sub-lists, bold-led
  paragraphs). Verified not live in the current corpus; recorded as a
  known limitation rather than fixed, since none of the three has ever
  occurred in a real help topic and guarding against them speculatively
  would add real complexity for zero present benefit.
- **Nitpick 9 (applied) — no tooling existed to write/refresh the
  baseline.** This became the `-update` flag (item 5 above), which also
  directly enabled a clean fix for BLOCKER 2 (regenerating all 51 entries
  with the new `english` field rather than trying to hand-splice it in).
- **Nitpick 10 (applied) — removed a redundant `sort.Strings` call** on
  `lib.Locales()`'s result, which `internal/manual.Library.Locales()`
  already returns pre-sorted.

**TDD-style revert/restore, done by the reviewer for real** (in an isolated
worktree): reverted just the `de/catalog.md` content fix, confirmed
`guard-help-drift.sh` failed with the exact original mismatch (2 missing
sub-bullets, 1 missing top-level bullet, 3 missing bold lead-ins, no
baseline entry), then restored the file and confirmed the guard passed
again. Caveat the reviewer noted and this record is passing forward: the
*other* half of the original bug (stale prose describing the pre-#1901 UI,
with unchanged structure) would not by itself be caught by a pure
structural guard — it only failed here because the same edit also changed
bullet counts. The tool's doc comment already says the guard "cannot prove
a translation is accurate"; this is a concrete instance of that limit, not
a new one.

**Baseline spot-check**: the reviewer independently reimplemented
`computeSignature` in Python and checked all 52 original entries (now 51)
against the real files — every recorded signature matched reality, every
entry still genuinely differed from English, and the `ut-docs#1973`
reference was consistent with no leftover placeholder.

**Final verdict: safe to merge.**
