#!/usr/bin/env bash
#
# #3048 / ADR-0125 Amendment 1: Stripe Terminal Tap to Pay is a COMPILE-TIME
# flag (-PtapToPay=true, arm64 flavor only), default OFF. A shipped build must
# never carry it by accident. Scanned: every workflow and composite action,
# scripts/, and gradle.properties files. Any assignment of tapToPay (as -P, in a
# properties file, as ORG_GRADLE_PROJECT_tapToPay, in an env block) to a value
# other than literally "false" fails, except android-ci.yml, whose compile/test
# steps may use "true" so long as the whole step (not only the line) is free of
# release, bundle, publish, upload, sign and attach words. Turning the flag on for
# a release is one line in release.yml in a card the owner signs; that card adds
# android/tap-to-pay-release-approved.txt, the marker this guard accepts.
# release.yml additionally checks the BUILT APKs (check-android-stripe-sdk.sh,
# guard-android-manifest-features.sh shipped), which no spelling can bypass.
#
# Usage: guard-android-tap-to-pay-flag.sh [repo-root] [approval-marker]
set -euo pipefail

ROOT="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}"
MARKER="${2:-${ROOT}/android/tap-to-pay-release-approved.txt}"
[ -d "${ROOT}" ] || { echo "guard-android-tap-to-pay-flag: ${ROOT} missing" >&2; exit 1; }

ROOT="${ROOT}" MARKER="${MARKER}" python3 - <<'PY'
import os
import re
import sys

root = os.environ["ROOT"]
approved = os.path.isfile(os.environ["MARKER"])

SELF = re.compile(r"(guard-android-tap-to-pay-flag|check-android-stripe-sdk)[^/]*$")
# tapToPay (any case, also ORG_GRADLE_PROJECT_tapToPay / -PtapToPay) assigned something
ASSIGN = re.compile(r"tap[_-]?to[_-]?pay[\"']?\s*[=:]\s*[\"']?([^\s\"'#]*)", re.IGNORECASE)
FORBIDDEN_IN_CI_STEP = re.compile(r"release|bundle|publish|upload|sign|attach", re.IGNORECASE)

files = []
for base, dirs, names in os.walk(root):
    dirs[:] = [d for d in dirs if d not in (".git", "node_modules", "build", ".gradle", "testdata")]
    rel = os.path.relpath(base, root)
    top = rel.split(os.sep)[0]
    for n in names:
        path = os.path.join(base, n)
        r = os.path.relpath(path, root)
        in_wf = r.startswith(os.path.join(".github", "workflows")) or r.startswith(os.path.join(".github", "actions"))
        in_scripts = top == "scripts" and not SELF.search(r)
        is_props = n == "gradle.properties" or n.endswith(".gradle.properties")
        if in_wf or in_scripts or is_props:
            files.append(r)

fail = []

def comment_stripped(line):
    return "" if line.lstrip().startswith("#") else line

def ci_steps(text):
    """Split a workflow into steps at '- name:' / '- uses:' / '- run:' list items."""
    steps, cur = [], []
    for line in text.splitlines():
        if re.match(r"^\s*-\s+(name|uses|run|id|env|if|with)\s*:", line) and cur:
            steps.append(cur)
            cur = []
        cur.append(line)
    steps.append(cur)
    return steps

for r in sorted(set(files)):
    try:
        text = open(os.path.join(root, r), encoding="utf-8", errors="replace").read()
    except OSError:
        continue
    lines = [comment_stripped(l) for l in text.splitlines()]
    hits = []
    for i, line in enumerate(lines, 1):
        for m in ASSIGN.finditer(line):
            val = m.group(1).strip()
            if val.lower() != "false":
                hits.append((i, line.strip(), val))
    if not hits:
        continue
    if os.path.basename(r) == "android-ci.yml":
        for step in ci_steps("\n".join(lines)):
            body = "\n".join(step)
            if ASSIGN.search(body) and any(m.group(1).lower() != "false" for m in ASSIGN.finditer(body)):
                if FORBIDDEN_IN_CI_STEP.search(body):
                    fail.append(f"{r}: a step that enables tapToPay mentions release/bundle/publish/upload/sign/attach")
        for i, line, val in hits:
            if val.lower() != "true":
                fail.append(f"{r}:{i}: tapToPay set to '{val}' (only literally true in compile/test steps, or false)")
        continue
    if approved:
        continue
    for i, line, val in hits:
        fail.append(f"{r}:{i}: tapToPay set to '{val}' (only literally false is allowed): {line}")

if fail:
    for f in fail:
        print("guard-android-tap-to-pay-flag: " + f, file=sys.stderr)
    print("Tap to Pay ships dark until an owner-signed card adds android/tap-to-pay-release-approved.txt (ADR-0125 Amendment 1, #3048).", file=sys.stderr)
    sys.exit(1)
print("guard-android-tap-to-pay-flag: OK")
PY
