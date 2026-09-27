# Review — docs-shots determinism: pixel diagnostics + diff artifact (ut-docs#2929)

Date: 2026-09-27 · Lane: `lane:cloud-24` · Branch: `fix/2929-docs-shots-determinism`
Author model: Opus 5.5 · Reviewer: Fable (independent subagent)

**Scope: part of ut-docs#2929, not its fix.** The card stays open; this PR
ships the tooling that located the nondeterminism and the evidence below.

## What shipped

- `scripts/ci/pngdiff` (Go, stdlib `image/png`): compares two PNGs and prints
  the differing-pixel count, the largest per-channel delta, the bounding box
  and row bands (so two unrelated regions show separately). Unit-tested.
- `scripts/ci/guard-docs-shots-determinism.sh`: on FAIL, prints the pngdiff
  summary under every differing PNG, and copies run A's and run B's copies
  into `$DOCS_SHOTS_DIFF_DIR` before the work dir is removed.
- `.github/workflows/docs-shots-determinism.yml`: sets `DOCS_SHOTS_DIFF_DIR`,
  uploads it as the `docs-shots-diff` artifact on failure (14 days), and adds
  `scripts/ci/pngdiff/**` to the `paths:` filter.

Why the log summary and not only the artifact: artifact downloads go to
`*.blob.core.windows.net`, which cloud pipeline lanes cannot reach; the job
log is readable through the GitHub API everywhere.

## Investigation findings (for the next cycle on #2929)

~45 `workflow_dispatch` runs of the guard on this branch and experiment
branches `exp/2929-*` (runs 36294477259 … 36296991047):

1. **One signature, every time.** Every mismatch, on any page and locale, is
   the same 25 pixels, max channel delta 38/255: `x=9..46 y=241..504` with
   bands `x=9 y=241..242` and `x=30..46 y=501..504` (RTL: mirrored,
   `x=956..993`). That is the rail's Orders (bell) button's lower-left corner
   arc and the help button icon's lower arc. Pages seen: fiscal-device,
   display, till-designer, designer — any page with the rail. Baseline failure
   rate ~70% of run pairs, 1–3 PNGs of 124 each.
2. **DOM state is identical.** A temporary trace (html attrs/classes, rail
   element rects to 1/100 px, rail computed transform/opacity/filter/
   will-change/z-index/contain/view-transition-name, running animations,
   `:hover`/focus, nav scroll metrics) was byte-identical between A and B for
   every mismatched file. Only the order and timing of the rail's
   `hx-trigger="load"` chip swaps (bugreport/sync/fiscal/diagnostics/session)
   differed.
3. **Variants that did NOT fix it** (each 3–6 runs, still failing with the
   same signature): rail `will-change: transform`; rail
   `view-transition-name: none`; `display:none` relayout of the rail or of
   `<body>` before capture; answering the five chip requests in one fixed
   order; `--num-raster-threads=1`; `--disable-threaded-animation
   --disable-threaded-scrolling --disable-checker-imaging …`;
   `--disable-software-rasterizer`; `--disable-features=SkiaGraphite,Vulkan`;
   a 1 px viewport-height bounce before capture (3/3 on an experiment branch,
   then 4/6 failures on this branch — chance, reverted here).
4. Locally (Chromium 141 in the sandbox, not the pinned 149) the full guard
   passes; the CI runner's pinned Chromium is where it reproduces.

Leads not yet tried: dump `chrome://gpu` from the CI browser to see which
raster/compositing path headless 149 really uses; compare pixel values of the
two states against a third capture to learn which state is "normal"; round the
rail's fractional geometry (`8.5`, `233.20`, `468.34`) to whole pixels and see
whether the two arcs stop flipping.

## Review findings

Fable ran gofmt/vet/tests/golangci-lint/shellcheck, the scripts guards, and
simulated the FAIL block under `set -euo pipefail` (DIFF_DIR set/unset/
unwritable, `go` absent, corrupt PNG, no mismatches): exit 1 in every failing
case, 0 only on no mismatch. Verdict: nothing blocking.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Low | A failing `mkdir`/`cp` into `DOCS_SHOTS_DIFF_DIR` aborted the FAIL report under `set -e` (still exit ≠ 0, but lost the remaining lines) | Fixed: best-effort copy, prints "could not copy" |
| 2 | Low | `go build … 2>/dev/null` hid why pngdiff output was missing | Fixed: compiler output reaches the log plus an explicit "pngdiff unavailable" line |
| 3 | Low | Size mismatch with identical overlap printed "bytes differ only in encoding" | Fixed: prints "overlap identical" |
| 4 | Nit | Comments said "every differing file"; MISSING-in-one-run files aren't copied | Fixed wording ("byte-differing") |
| 5 | Nit | No tests at the bandGap boundary / alpha-only / `main()` | Accepted — reviewer verified by hand; diagnostic tool |

Verdict: safe to merge as diagnostics. #2929 itself stays open.

## Verified beyond automated tests

- The tooling ran for real on CI: run 36294478224 printed the pixel summary
  and uploaded `docs-shots-diff` (4 files); the traced runs above used it.
- pngdiff tests were mutation-checked: widening band merging and
  double-counting pixels each make a test fail; restored → pass.
- `gofmt`, `go vet`, `go test`, `golangci-lint` (0 issues), `shellcheck`, and
  the `ci.yml` build-job guards: all clean except
  `guard-deadcode-baseline.sh` (fails identically on `main` in this sandbox —
  no GTK/WebKit headers) and `guard-shellcheck-version.sh` (sandbox has
  shellcheck 0.11.0) — environment only.
- No UI surface changed; no screenshots regenerated; no locale keys.
