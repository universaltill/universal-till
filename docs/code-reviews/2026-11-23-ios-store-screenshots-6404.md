# track3 review (ut-docs#6404, iOS screenshots)
Scope: ut-ios TillWebView.swift + ios-start-path-guard_test.sh; ut-docs scripts/ios-assets/*. Reviewer ran check_test.sh and the guard test (no simulator).
Pre-flight: no commit/push/simulator run; no real shop name (Corner Shop demo), no secrets.

Findings
1. (medium, fixed) Start path accepted anything starting with "/" not "//"; a backslash or odd form could resolve off-origin. Now the resolved URL must match base scheme/host/port. Guard test pins the check.
2. (low, fixed) generate.sh recorded a sim as "booted by us" even if it was already running, so cleanup could shut down a user's simulator. Now only recorded when it was not Booted.
3. (low, fixed) check.sh took the shop name only from build-info.txt (written by the generator from a constant). Now also requires a captured page to contain "Corner Shop"; fixture added.
Accepted/known: channel is read from TillServer.swift source, not the running app (stated in generate.sh); the guard test is a grep/awk pin, weaker than a compile of Release (CI Release build is the real proof). Hook is under #if DEBUG && targetEnvironment(simulator): not compiled into Release or device.
Verified: seeds only via UT_DATA_DIR=<app container>/Library/Application Support/till; simctl calls all by UDID; no pkill/killall; set -e/pipefail traps fine; check_test 10/10 pass.
Verdict: OK to commit. README already states why no PNGs are committed.
