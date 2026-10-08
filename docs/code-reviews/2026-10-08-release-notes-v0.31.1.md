# Review — v0.31.1 release notes (en/de/tr/ar/fa)

- **Date:** 2026-10-08
- **Lane:** lane:cloud-41
- **Written by:** Claude Opus 5.5
- **Reviewed by:** Claude Fable (independent subagent)

These notes cover the 6 first-parent merges since v0.31.0: "Update now" for a
joined till from the main till (#1761), plugin file fields (#1766), plugin
schedules (#1758), photos inside backups (#1768), and the legacy-registration
credential fix (#1757).

**Left out** (internal only): #1765 (iOS WASM cost diagnostic).

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| M1 | must | "the same button tries again" read as an automatic retry; it is one attempt per press. Also missing: works with automatic updates off, installs once no sale is open | **Fixed** in all five files |
| M2 | must | Schedules: "every few minutes" and "never runs in the middle of a sale" overstated (lower bound 30 s, no upper bound; a 3 s pause after each sale, not a guarantee); permission revoke omitted | **Fixed** in all five files |
| S1 | should | Arabic said جهاز (device) for till; UI and v0.31.0 say صندوق | **Fixed** |
| S2 | should | German "Führungskraft" vs "Manager" in every earlier note | **Fixed** |
| S3 | should | Fixed bullet overstated "stay connected" — sync still returns 402 until the subscription is active; the win is not being stranded later | **Fixed** in all five files |
| S4 | should | "such as a price list" / "declares": matched the shipped manual ("such as a photo", "sets") | **Fixed** in all five files |
| N1 | nit | de "Belegslogo" vs "Beleglogo" | Left: matches the shipped de help topic |

`guard-release-notes.sh v0.31.1` and `go test ./internal/releasenotes/` pass.

**Verdict:** safe to merge.
