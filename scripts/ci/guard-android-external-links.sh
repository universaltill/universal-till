#!/usr/bin/env bash
#
# ut-docs#1647: on a real Android till the product owner tapped
# "View on GitHub" on /my-reports and NOTHING happened — no browser, no
# navigation, no error. The cause was a one-line asymmetry in
# MainActivity's WebViewClient:
#
#     if (target.authority != allowedHost) {
#         return true // block: refuse to navigate off-origin
#     }
#
# `return true` tells the WebView "the app has handled this navigation" —
# and the app then did nothing with it. Every off-origin link in the till
# UI was therefore silently dead on Android, while macOS
# (cmd/unitill-desktop/webkit_darwin.go) had opened them in the system
# browser all along.
#
# The origin confinement itself must STAY (ut-docs#1254): window.AndroidKiosk
# is injected into every page this WebView shows, so letting the WebView
# itself navigate off-origin would hand the kiosk-unlock bridge to content
# this app never authored. The fix is the missing *else* branch — hand the
# URL to the system browser as a separate task — not a relaxed block. So
# this guard pins BOTH halves: the WebView still never loads an off-origin
# page, AND an off-origin http(s) link still reaches the browser.
#
# Static source check, not an instrumented Android test: this repo has no
# Android test/CI job (release.yml's android-app job only runs at release
# time, with an SDK/NDK this environment doesn't have) — a source-level
# guard running in the normal `go`-only CI is the cheap, always-on
# equivalent. Same rationale as guard-android-status-address.sh.
#
# Checks are scoped to the two FUNCTION BODIES that matter, not to the
# file as a whole. That is load-bearing, not tidiness: the first draft of
# this guard grepped the whole file and silently passed two of the
# regressions its own test plants, because `authority != allowedHost` also
# occurs in onPermissionRequest and `return true` occurs in four unrelated
# places. Comments are stripped first, so a reverted fix can't hide behind
# prose that still describes it (same lesson as guard-kiosk-launch-flags.sh).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

python3 - <<'PY'
import re
import sys

PATH = "android/app/src/main/java/com/universaltill/pos/MainActivity.kt"
failures = []


def fail(*lines):
    failures.append("\n   ".join(lines))


try:
    src = open(PATH, encoding="utf-8").read()
except OSError as e:
    sys.exit(f"❌ android-external-links guard: cannot read {PATH}: {e}")

# Comment lines out, keeping line structure. Only whole-line comments are
# dropped; a trailing `// ...` on a code line is harmless to these checks
# and stripping it would risk eating a `//` inside a URL literal.
code = "\n".join(
    "" if re.match(r"\s*(//|\*|/\*)", line) else line for line in src.splitlines()
)


def body_of(signature):
    """The braced body following `signature`, or None if absent.

    String literals are blanked before brace counting so a `{` inside a
    Kotlin string template can't unbalance the scan.
    """
    start = code.find(signature)
    if start == -1:
        return None
    open_brace = code.find("{", start)
    if open_brace == -1:
        return None
    scannable = re.sub(r'"(?:\\.|[^"\\])*"', '""', code[open_brace:])
    depth = 0
    for i, ch in enumerate(scannable):
        if ch == "{":
            depth += 1
        elif ch == "}":
            depth -= 1
            if depth == 0:
                return code[open_brace : open_brace + i + 1]
    return None


nav = body_of("override fun shouldOverrideUrlLoading(")
if nav is None:
    fail(
        f"{PATH} has no shouldOverrideUrlLoading override — this WebView is no",
        "longer confined to the till's own origin at all, which is what keeps",
        "window.AndroidKiosk out of reach of third-party pages (ut-docs#1254).",
    )
    nav = ""

browser = body_of("private fun openInSystemBrowser(")
if browser is None:
    fail(
        f"{PATH} has no openInSystemBrowser() — an off-origin link would be",
        "blocked and then silently dropped, which is exactly the",
        "'View on GitHub does nothing' bug (ut-docs#1647).",
    )
    browser = ""

# 1. The origin confinement is still there, inside the navigation callback
#    specifically. Without it the rest is moot: the WebView would navigate
#    off-origin itself and the browser hand-off would never be reached.
if not re.search(r"authority\s*!=\s*allowedHost", nav):
    fail(
        f"{PATH}'s shouldOverrideUrlLoading no longer compares the requested",
        "URL's authority against allowedHost — the WebView is no longer",
        "confined to the till's own origin, which is what keeps",
        "window.AndroidKiosk out of reach of third-party pages (ut-docs#1254).",
    )

# 2. The off-origin branch actually reaches the hand-off. A hand-off that
#    exists but is never called is the same dead link with more code.
if "openInSystemBrowser(target)" not in nav:
    fail(
        f"{PATH}'s shouldOverrideUrlLoading does not call",
        "openInSystemBrowser(target) — an off-origin link is blocked and then",
        "dropped, so the link is still dead (ut-docs#1647).",
    )

# 3. That branch still returns true (handled). Returning false would hand
#    the off-origin URL back to the WebView to load in place — the
#    ut-docs#1254 hole, reopened from the other direction. Checked as the
#    statement immediately following the hand-off, because `return true`
#    occurs several times elsewhere in this file for unrelated reasons.
if not re.search(r"openInSystemBrowser\(target\)\s*\n\s*\}?\s*\n?\s*return\s+true\b", nav):
    fail(
        f"{PATH}'s off-origin branch no longer returns true immediately after",
        "handing the URL to the browser — returning false (or falling",
        "through) lets the WebView load the off-origin page in place, with",
        "window.AndroidKiosk still injected (ut-docs#1254).",
    )

# 3b. The hand-off is main-frame only. shouldOverrideUrlLoading also fires
#     for subframes, so without this an off-origin <iframe> anywhere in the
#     till UI would launch the browser on every page load, over the live
#     sale screen.
if not re.search(r"isForMainFrame\s*==\s*true", nav):
    fail(
        f"{PATH}'s off-origin branch no longer restricts the browser hand-off",
        "to main-frame navigations — an off-origin <iframe> on any page would",
        "spawn a browser on every page load, unprompted, over the live sale",
        "screen (ut-docs#1647).",
    )

# 4. Only http/https are ever forwarded. Anything else reaching ACTION_VIEW
#    turns an in-page link into "launch whatever app claims this scheme",
#    including Android's own intent: scheme, which can name a component.
if not re.search(r'scheme\s*!=\s*"http"\s*&&\s*\w+\s*!=\s*"https"', browser):
    fail(
        f"{PATH}'s openInSystemBrowser no longer refuses every scheme except",
        "http and https — an intent:// or custom-scheme URL from web content",
        "would reach Intent.ACTION_VIEW, which is an arbitrary app-launch",
        "primitive available to any page that can render a link (ut-docs#1647).",
    )

# 5. The hand-off is a separate task, so the till's own Activity is not
#    replaced. "Opens in another browser instance, does not change the
#    current POS screen" is the requirement in the owner's own words.
if "FLAG_ACTIVITY_NEW_TASK" not in browser:
    fail(
        f"{PATH}'s openInSystemBrowser launches the URL without",
        "FLAG_ACTIVITY_NEW_TASK — the browser would land in the till's own",
        "task stack instead of alongside it, so returning to the POS screen",
        "is no longer a matter of leaving the browser (ut-docs#1647).",
    )

# 6. A failed hand-off must not fail silently. A swallowed exception here
#    reproduces, precisely, the "I tapped it and nothing happened" symptom
#    this whole ticket exists to remove.
if "external_link_failed" not in browser:
    fail(
        f"{PATH}'s openInSystemBrowser no longer surfaces a failed hand-off",
        "via R.string.external_link_failed — a device with no browser app",
        "would silently do nothing, which is the exact symptom this ticket",
        "fixed (ut-docs#1647).",
    )

if failures:
    for f in failures:
        print(f"❌ android-external-links guard: {f}", file=sys.stderr)
    sys.exit(1)

print(
    "✓ android-external-links guard: off-origin links blocked in-WebView and "
    "handed to the system browser"
)
PY
