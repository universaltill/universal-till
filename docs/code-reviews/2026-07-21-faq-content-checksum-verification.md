# 2026-07-21 — Verify FAQ (and future page-plugin) content bundle checksums

## Context
Companion fix in `ut-plugin-faq` (same session): every `content/<locale>.json`
now carries a real `checksum_sha256` (was `""`), computed by
`scripts/checksum.py` via byte-level placeholder substitution (the field
zeroes its own value to a 64-char placeholder before hashing, since it can't
hash its own bytes as-is). This change adds the till-side half: actually
verifying it when a page plugin's content bundle is loaded.

## Changes
- `internal/pages/plugin_page.go`: `checksumValid(raw []byte) bool` — same
  byte-level substitution scheme as the Python packaging tool (deliberately
  not JSON re-serialization, to avoid any Go/Python formatting mismatch).
  Wired into `loadContentBundle`: a mismatch logs via `logging.L().Warnf`
  (feeds the existing warn/error ring buffer already surfaced through the
  till's heartbeat digest → merchant portal Problems feed — reusing existing
  plumbing, not a new alerting path) and the bundle is refused, falling
  through to the existing `content/index.html` / empty-state chain. A bundle
  with no populated checksum field (older content, or a plugin author who
  hasn't adopted the convention) is treated as nothing-to-verify and passes
  through unchanged — defense-in-depth against post-install on-disk
  corruption, not a replacement for the Ed25519 whole-bundle signature
  already verified at install time.
- Cross-language proof: ran this exact Go logic against all 9 real
  `ut-plugin-faq/content/*.json` files (post their own fix) — all 9 verify
  `true`.
- Tests: `TestPluginPage_ChecksumValidBundleRenders` (matching checksum
  renders normally), `TestPluginPage_ChecksumMismatchRefusesToRender`
  (content tampered after signing → refused, tampered text never reaches the
  response body).

## Independent review
One review pass (3 angles: line-by-line/regex correctness, cross-file
caller/fixture trace, failure-mode/observability). Findings and
disposition:

**Fixed:**
- Hex class was lowercase-only (`[0-9a-f]{64}`) — a valid but uppercase
  digest (e.g. hand-edited, or a future tool using `.hexdigest().upper()`)
  would silently skip verification instead of being checked. Made the
  pattern case-insensitive and lowercase both sides before comparing.
- `want` was read from the first regex match while `ReplaceAll` zeroed
  *every* match — correct only when the field appears exactly once. Changed
  to `FindAllSubmatch(raw, 2)`: 0 matches → nothing to verify, 1 → verify
  normally, 2+ → refuse (a duplicate `checksum_sha256` key is itself a sign
  of a malformed/tampered file; refusing is consistent with fail-closed,
  never picks an arbitrary occurrence as "the" checksum). Note: reviewer
  confirmed the pre-fix asymmetry could only ever fail *closed* (reject a
  legitimate file), never fail *open* (accept tampered content) — not a
  security hole, but worth tightening since it's a one-line fix.

**Considered, deliberately not changed:**
- *On checksum failure, `loadContentBundle` gives up entirely instead of
  trying another candidate locale file the same plugin ships (e.g. `en-GB`
  intact next to a corrupted `en-US`).* Real gap, but a bigger structural
  change to the locale-picking loop (would need to try every candidate in
  fallback order until one both parses and verifies, not just pick once).
  Logged as a follow-up rather than rushed through under time pressure —
  today's behavior (refuse → generic "no page content" fallback) is safe,
  just not maximally available.
- *A checksum failure is visually indistinguishable from a plugin that
  simply ships no content page.* True, but a distinct error banner is a
  UI/UX change beyond this fix's scope.
- *The warning only lives in the in-memory `logging.Recent()` ring buffer
  (capped, no disk persistence) until the next heartbeat sample.* This is
  the same mechanism the existing persistent Problems/logs feed
  (`ProblemEvent`, ADR-0018 2a) already reads from — the correct existing
  integration point, not a shortcut. Building separate durable storage for
  this one warning would duplicate that system.
- The repo's own `data/plugins/com.universaltill.ut-faq/*/content/en-US.json`
  sample fixtures (pre-existing, gitignored `data/plugins/` per
  `.gitignore` — confirmed untracked, not shipped) still carry the old
  empty checksum; harmless (empty → "nothing to verify"), and out of scope
  to regenerate here since they're local dev-state, not repo content.

## Verification
`go build ./...`, `go test ./...`, `scripts/ci/guard-data-access.sh` — all
green. Cross-checked all 9 real `ut-plugin-faq` locale bundles verify `true`
against this implementation.

## Follow-up (not done here)
Try remaining candidate locale files on a checksum failure instead of
giving up immediately — needs restructuring `loadContentBundle`'s
single-`pick` selection into an ordered-candidates loop.
