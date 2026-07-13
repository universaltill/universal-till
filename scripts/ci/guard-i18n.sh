#!/usr/bin/env bash
#
# Enforces multilingual UI (docs reference/coding-standards.md):
#   1. Every {{ T "key" }} used in a template must exist in web/locales/en.json.
#   2. Every locale file must define exactly the same key set as en.json
#      (the base locale) — no missing translations, no orphan keys.
# Fails CI so a new page/string can't ship untranslated.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

BASE="web/locales/en.json"
[ -f "$BASE" ] || { echo "guard-i18n: $BASE missing" >&2; exit 1; }

python3 - <<'PY'
import glob, json, re, sys

fail = False

def keys(path):
    return set(json.load(open(path)).keys())

base = keys("web/locales/en.json")

# 1. template T "key" usage must resolve in the base locale.
used = set()
for f in glob.glob("web/ui/**/*.html", recursive=True):
    used |= set(re.findall(r'\{\{-?\s*T\s+"([^"]+)"', open(f).read()))
# Go-side menu labels (nav.*) live in the base menu; assume any nav.* is used.
missing = sorted(k for k in used if k not in base)
if missing:
    fail = True
    print("guard-i18n: template keys missing from en.json:")
    for k in missing:
        print("  -", k)

# 2. every locale file must match the base key set exactly.
for path in sorted(glob.glob("web/locales/*.json")):
    if path.endswith("en.json"):
        continue
    ks = keys(path)
    only_base = sorted(base - ks)
    only_loc = sorted(ks - base)
    if only_base or only_loc:
        fail = True
        print(f"guard-i18n: {path} differs from en.json:")
        for k in only_base:
            print("  missing:", k)
        for k in only_loc:
            print("  orphan :", k)

if fail:
    sys.exit(1)
print(f"✓ i18n guard: {len(used)} template keys resolve; all locales match en.json")
PY
