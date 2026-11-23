#!/usr/bin/env bash
# ut-docs#6404: the -UTStartPath launch argument (store-screenshot capture)
# must exist only inside `#if DEBUG && targetEnvironment(simulator)`, so no
# Release, TestFlight or device build can open an arbitrary start path.
set -euo pipefail
f="$(cd "$(dirname "$0")/../.." && pwd)/ios/UniversalTill/TillWebView.swift"
fail=0
n="$(grep -c 'UTStartPath' "$f" || true)"
[ "$n" -gt 0 ] || { echo "FAIL: no UTStartPath hook"; fail=1; }
# Every UTStartPath use lies between the guard's #if and the next #endif.
awk '/#if DEBUG && targetEnvironment\(simulator\)/{g=1} /#endif/{g=0}
     /UserDefaults.*UTStartPath/ && !g {print "FAIL: unguarded read at line " NR; bad=1}
     END{exit bad}' "$f" || fail=1
grep -q 'hasPrefix("/"), !path.hasPrefix("//")' "$f" || { echo "FAIL: start path must be same-origin relative"; fail=1; }
[ "$fail" = 0 ] && echo "ios-start-path-guard: ok"
grep -q 'url.host == base.host' "$f" || { echo "FAIL: resolved start URL must be origin-checked"; fail=1; }
exit "$fail"
