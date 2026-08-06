# Review — bug-report panel: screenshot capture (till side)

Ticket: universaltill/ut-docs#347
Date: 2026-08-06
Branch: `feat/347-screenshot-capture-till` → `main`
Reviewer model: Opus (deliberately different from the model that wrote the diff)

## What shipped

Still-screenshot capture added to the existing non-modal bug-report panel
(ut-docs#346), alongside its voice-note and screen-recording capture. Plural
and removable — several screenshots ride along with one report.

- `web/ui/partials/bugreport_panel.html` — a full-width row below the existing
  two-column recorder grid: a "Take screenshot" button plus a wrapping strip of
  thumbnails, each with its own remove button. Capture uses
  `getDisplayMedia` + `ImageCapture.grabFrame()`, with a hidden
  `<video>` + `<canvas>` fallback for browsers without `ImageCapture`; the
  stream is torn down after one frame. Blobs are appended to the existing
  `FormData` as repeated `image` parts.
- `internal/issuereport/bundle.go` — `Save` grew an `images [][]byte`
  parameter, written as `image-0.png`, `image-1.png`, … in call order.
  `Bundle` grew `ImagePaths []string`; `Pending()` globs them back and sorts on
  the parsed numeric index via the new `imageIndex` helper.
- `internal/pages/issue_report_page.go` — `POST /api/issue-reports` reads
  repeated parts from `r.MultipartForm.File["image"]` (rather than
  `r.FormFile`, which only ever returns the first match), applying the same
  `readCappedOrReject` treatment as audio/video.
- `internal/cloudsync/issue_reports.go` — one repeated `image` multipart field
  per screenshot, in `ImagePaths` order, on the existing retry cadence.
- 5 new locale keys × 4 locale files, `.bugreport-thumb*` CSS, and updated
  `web/help/{en,fa,ar,tr}/bug-reporting.md` prose.

The cloud-side storage half is a separate card in `ut-cloud` and was **not**
reviewed here.

## What the review found

### 1. Fixed: the numeric image sort was guarded by no test at all

`imageIndex` and the `sort.Slice` in `Pending()` exist for exactly one reason,
stated in their own doc comment: `filepath.Glob` returns lexicographic order,
which puts `image-10.png` before `image-2.png`, so a report with 10+
screenshots would reach the cloud out of capture order. Capture order is the
whole value of a screenshot sequence ("then I clicked here, then this
happened").

The implementation is **correct** — I confirmed the ordering empirically at
n=12:

```
glob order : [image-0 image-1 image-10 image-11 image-2 … image-9]
ImagePaths : [image-0 image-1 image-2 … image-9 image-10 image-11]
```

But nothing tested it. `TestSaveWithImagesRoundTrip` uses three images, where
numeric and lexicographic order are identical — and its comment nonetheless
claims the case covers "image-10 before image-2 once double digits are in
play", which it cannot. Proven by deleting the `sort.Slice` line and re-running
the whole committed suite: `internal/issuereport`, `internal/pages` and
`internal/cloudsync` all stayed green (only the pre-existing uid-0 flake
below). The invariant would have regressed silently.

Added `TestPendingOrdersImagesNumericallyNotLexically` (12 images, asserting
both path order and that path order agrees with content order), and corrected
the misleading comment on the 3-image case to point at it. Confirmed real:
with the sort removed the new test fails with
`ImagePaths[2] = …/image-10.png, want …/image-2.png — images are in
lexicographic, not capture, order`; restored, it passes.

### 2. Fixed: `TestIssueReportAPI_OversizedImageRejectsAndSavesNothing` was a false pass

Its comment claims it exercises the per-image cap ("same 'no silent
corruption' contract as audio/video"). It does not. `http.MaxBytesReader`
bounds the whole body at `issueReportMaxBytes`, and the per-part cap is *also*
`issueReportMaxBytes` — so a single part can never exceed the cap without the
body having exceeded it first. The 400 comes from `ParseMultipartForm`, and
the image loop is never reached.

Demonstrated the same way as above: with the image handling stripped out of
the handler, this test still passes. The **outcome** it asserts (reject, save
nothing) is correct and worth keeping, so I left the test and rewrote the
comment to state precisely what it guards and what it does not, rather than
leave a claim in the suite that a future reader would trust.

Corollary, noted not fixed: the per-image `readCappedOrReject` call is
unreachable defensive code. So are the pre-existing audio and video ones, for
the same reason — this diff copied an existing pattern rather than introducing
a new problem, and the copy is harmless. Not worth churning here.

### 3. Fixed: removable thumbnails leaked their object URLs

`addScreenshotThumb` called `URL.createObjectURL(blob)` and never revoked it.
Unlike the voice and screen previews — one each, overwritten on re-record, and
not removable — screenshots are explicitly add-and-remove, so a manager
retaking a shot several times keeps every rejected full-resolution PNG pinned
in memory for the life of the document. On a till that stays on one page for a
whole trading day, that accumulates. Added `URL.revokeObjectURL` in the remove
handler.

### 4. Deferred: the `ImageCapture` fallback path can hang

In `grabFrameFromStream`, the fallback branch waits on a `loadedmetadata`
event. If that event never fires — the user cancels at the OS picker in a way
that yields a dead track, or the stream ends before metadata arrives — `done`
is never called, so the display stream is never stopped **and** the capture
button stays disabled forever. Both other paths (`grabFrame` rejection,
`play()` rejection) correctly funnel through `done(null)`.

Not fixed here: the correct fix is a timeout race, which is a real behavioural
decision (how long? what does the operator see?) rather than a mechanical
one, and the branch is unreachable on the browser the till actually ships
(Chromium has `ImageCapture`, so the primary path is always taken). Worth a
follow-up card if a non-Chromium till target ever appears.

### 5. Pre-existing, noted only

- `internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure` fails in this
  container. Confirmed environmental, not caused by this diff: the container
  runs as uid 0 (`id -u` → `0`), so the test's `0o500` directory does not
  actually block a write. Same finding as the ut-docs#346 review.
- Screenshots are not cleared from the panel after a successful save, so
  pressing Save twice sends them again in a second bundle. Exactly the existing
  behaviour of `voiceBlob`/`screenBlob`, unchanged by this diff.
- `make docs-shots` was not regenerated. Pre-accepted, already tracked as
  universaltill/ut-docs#364 (browser version mismatch); explicitly not treated
  as a finding here.
- `scripts/ci/guard-help-topics.sh`, referenced by `CLAUDE.md`, does not exist
  in this repo. Not this card's business — and moot here, since
  `bug-reporting.md` already existed with its `routes:` declared and this diff
  only changed prose.

## What I verified personally (not taken on trust)

- **TDD claim re-verified by reverting the implementation myself**, not by
  reading the author's report. Rather than `git stash` the whole files — which
  changes `Save`'s signature and collapses everything into compile errors I'd
  have had to dismiss — I surgically reverted only the *behaviour*, keeping the
  signatures, so every failure is a real assertion:
  - `TestSaveWithImagesRoundTrip` → `expected 3 ImagePaths, got 0: []`
  - `TestIssueReportAPI_SavesMultipleImages` → `expected 3 ImagePaths, got 0: []`
  - `TestUploadIssueReportSendsMultipleImageParts` → `got 0 image parts, want 3`

  All three fail for the right reason and pass once restored. Two of the six
  new cases (`TestSaveWithoutImagesLeavesImagePathsEmpty`,
  `TestIssueReportAPI_ZeroImagesUnaffected`) pass either way, which is correct
  — they are no-regression guards on the zero-screenshot path. The sixth is
  finding 2 above. Restoration confirmed byte-exact by re-checking
  `git diff --stat` against the pre-revert numbers.
- **The missing-`os.MkdirAll` class does not apply.** `Save` calls
  `os.MkdirAll(dir, 0o755)` before `saveBundleFiles`, and `saveBundleFiles` is
  unexported with `Save` as its only caller — so the image writes cannot be
  reached with no directory. The image loop also sits *after* the audio/video
  writes and *before* `meta.json`, so a failed image write still trips the
  existing `os.RemoveAll(dir)` cleanup and leaves no meta-less directory.
- **The cwd-relative-path class does not apply.** Every new write is
  `filepath.Join(dir, …)` where `dir` descends from `PendingDir` (production
  sets it from `paths.Data(...)` during Init). Nothing new writes outside the
  existing `PendingDir` mechanism; grepped the diff for `os.WriteFile` /
  `os.MkdirAll` / `os.Create` and the only non-test hit is the image loop.
- **Multipart handling is correct Go.** `r.MultipartForm` is guaranteed
  non-nil once `ParseMultipartForm` has returned without error, which it has
  30 lines above; the `!= nil` guard is harmless defence, not load-bearing, and
  it is correct as written. `r.MultipartForm.File["image"]` is the right way to
  read a repeated file field — `r.FormFile` returns only the first. File
  handles are closed on both the success and the size-rejection path (`f.Close()`
  before the `http.Error` return), so no descriptor leaks in the loop.
- **Disk use stays bounded** despite there being no cap on the *number* of
  screenshots: `MaxBytesReader` bounds the whole request at 32 MiB, so one
  report cannot exceed that regardless of how many stills it carries.
- **Offline-first**: read `issuereport.Save` and the whole package —
  `grep -rE "http\.|net/|url\.|Dial" internal/issuereport/*.go` returns
  nothing. Capture and local save do not touch the network; upload stays the
  separate best-effort `cloudsync` step. No modal blocker added to the kiosk
  flow — the panel remains non-modal.
- **JS double-click race**: the screenshot button sets
  `screenshotBtn.disabled = true` **synchronously**, as the first statement of
  the click handler, before the `getDisplayMedia` await. A disabled button
  dispatches no click, so a rapid double-click cannot open two pickers. This is
  a stronger guard than the `voiceStarting`/`screenStarting` flags it sits next
  to, not a missing one. Re-enabled on every exit: the `done` callback, the
  `!blob` branch and the `.catch`.
- **Stream teardown on both paths**: `stream.getTracks().forEach(t => t.stop())`
  runs inside the shared `done` callback, so it fires for the `ImageCapture`
  success, the `grabFrame` rejection, the fallback `<video>` success and the
  fallback `play()` rejection alike. The screen-share indicator does not stay
  lit after a single still — with the one exception in finding 4.
- **i18n by guard, not by eye**: `guard-i18n.sh` green — 839 template keys
  resolve, all locales match `en.json`. All 5 new `issuereport.screenshot_*`
  keys present in en/fa/ar/tr. Read the fa/ar/tr values: they are genuine
  translations in each language, matching the style of the neighbouring
  `screen_*` keys (e.g. ar `تعذّر التقاط لقطة الشاشة.` mirrors the existing
  `تعذّر بدء تسجيل الشاشة.`; tr `Ekran görüntüsünü kaldır` carries the correct
  accusative suffix), not English copied across. Every new visible string in the
  template goes through `{{ T }}`; the only literal is the `✕` glyph on the
  remove button, which carries a translated `title` and `aria-label`.
- **RTL**: the new CSS uses `inline-size`, `block-size`, `inset-block-start`,
  `inset-inline-end` only — grepped the added lines for `left`/`right`/
  `margin-left`/`padding-right`/`text-align: left|right` and found none.
- **Manual prose read in all four locales, not just English.** Each is written
  in its own language and — the detail that makes it actually usable — each
  quotes *its own locale's* button label verbatim: en "📷 Take screenshot",
  fa «📷 گرفتن اسکرین‌شات», ar «📷 التقاط لقطة شاشة», tr "📷 Ekran görüntüsü al",
  matching that locale's `issuereport.screenshot_capture` exactly. Each also
  states the plural ("you can add more than one"). Accurate, not cosmetic. One
  small gap: none of the four mentions that a thumbnail can be *removed*
  before saving. Minor, not a merge blocker.
- **No SQL anywhere in the diff** (grepped for SELECT/INSERT/UPDATE/DELETE/
  CREATE TABLE in added lines — none; `guard-data-access.sh` green). This card
  touches no persistence layer.
- **README** checked for staleness: it makes no claim about bug reporting,
  `/report-issue` or screenshots, so nothing went stale and no edit was owed.
- No real client or shop name and no secret-shaped value anywhere in the diff.

### Commands, after my fixes

```
$ go build ./...            → clean
$ go vet ./...              → clean
$ bash scripts/ci/guard-data-access.sh
✓ data-access guard: no inline SQL outside internal/data / internal/db
$ bash scripts/ci/guard-i18n.sh
✓ i18n guard: 839 template keys resolve; all locales match en.json; no
  hardcoded Go-side response strings found; no hand-written hx-vals literals found
$ go test ./...
--- FAIL: TestSaveCleansUpDirectoryOnWriteFailure (0.00s)
    bundle_test.go:171: expected Save to fail on a read-only bundle directory
FAIL	github.com/universaltill/universal-till/internal/issuereport	0.025s
(every other package ok — this is the documented uid-0 environment flake)
```

## Verdict

**Safe to merge.** The implementation itself is sound: no missing `MkdirAll`,
no cwd-relative path, correct repeated-multipart handling, correct stream
teardown, a real double-click guard, correct numeric ordering at double digits,
and no network on the capture/save path.

The three things I changed were all in the *test and JS hygiene* layer rather
than the shipped Go behaviour: an invariant with a comment but no test, a test
whose comment claimed more than it verified, and a leaked object URL. Two of
those were only visible by reverting the implementation and watching which
tests failed to notice — which is why that step was done by hand rather than
taken from the author's report. Full gate re-run green after the fixes.

Deferred: the `ImageCapture` fallback hang (finding 4), the manual's silence on
removing a thumbnail, and the pre-accepted `docs-shots` regeneration gap
(ut-docs#364).
