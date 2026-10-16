#!/usr/bin/env bash
#
# Self-test for guard-i18n-meta.sh (ADR-0160 §2-§4, ut-docs#4362). Runs in a
# throwaway sandbox copy (ut-docs#4311): each case rewrites the metadata,
# the area map, the baseline or en.json there, runs the guard, and restores
# the files byte-for-byte. The sandbox is its own git repo, so the base-ref
# checks (placeholder change, shrink-only baseline) run against a commit
# made here, never the real origin/main.
set -euo pipefail

# shellcheck source=scripts/ci/guard-test-sandbox.sh
source "$(dirname "${BASH_SOURCE[0]}")/guard-test-sandbox.sh"
enter_guard_test_sandbox web/locales web/i18n-meta

GUARD="scripts/ci/guard-i18n-meta.sh"
FILES=(web/locales/en.json web/i18n-meta/en.meta.json web/i18n-meta/areas.json scripts/ci/i18n-baseline/key-meta-baseline.txt)
FAILS=0
OUT="$(mktemp)"
BACKUP="$(mktemp -d)"
for f in "${FILES[@]}"; do cp "$f" "${BACKUP}/$(basename "$f")"; done
restore() { for f in "${FILES[@]}"; do cp "${BACKUP}/$(basename "$f")" "$f"; done; }
cleanup() {
  local status=$?
  rm -rf "${BACKUP}" "${OUT}"
  remove_guard_test_sandbox
  exit "${status}"
}
trap cleanup EXIT

g() { git -c user.name=t -c user.email=t@example.invalid -c core.hooksPath=/dev/null -c commit.gpgsign=false "$@"; }
g commit -qm base
g tag base

# expect WANT_EXIT LABEL [MESSAGE_FRAGMENT] — runs the guard against the
# sandbox's "base" tag, then restores every file.
expect() {
  local want="$1" label="$2" frag="${3:-}" got=0
  I18N_META_BASE=base bash "${GUARD}" >"${OUT}" 2>&1 || got=1
  if [ "${got}" != "${want}" ]; then
    echo "❌ FAIL: ${label} (exit ${got}, want ${want})" >&2
    cat "${OUT}" >&2
    FAILS=$((FAILS + 1))
  elif [ -n "${frag}" ] && ! grep -qF -- "${frag}" "${OUT}"; then
    echo "❌ FAIL: ${label}: output lacks '${frag}'" >&2
    cat "${OUT}" >&2
    FAILS=$((FAILS + 1))
  else
    echo "✓ ${label}"
  fi
  restore
}

# py SCRIPT — edits the sandbox files with Python (json in, json out).
py() { python3 -c "$1"; }
EDIT_META='import json,sys
p="web/i18n-meta/en.meta.json"; m=json.load(open(p))
'
SAVE_META='
json.dump(m, open(p,"w"), indent=1, ensure_ascii=False)'
ADD_KEY='import json
p="web/locales/en.json"; d=json.load(open(p)); d.update(NEW); json.dump(d, open(p,"w"), indent=2, ensure_ascii=False)'

# --- the real files pass ---
expect 0 "the real metadata passes"

# --- check 1: a new key without metadata ---
py "NEW={\"common.zz_new\": \"New\"}; ${ADD_KEY}"
expect 1 "a new key without a description fails" "common.zz_new has no metadata"

py "NEW={\"common.zz_new\": \"New\"}; ${ADD_KEY}"
py "${EDIT_META}m[\"common.zz_new\"]={\"description\":\"x\"}${SAVE_META}"
expect 1 "a new key with a description but no review fails" "common.zz_new has no metadata"

py "NEW={\"common.zz_new\": \"New\"}; ${ADD_KEY}"
py "${EDIT_META}m[\"common.zz_new\"]={\"description\":\"A new button.\",\"review\":\"standard\"}${SAVE_META}"
expect 0 "a new key with description and review passes"

# --- check 2: placeholders ---
py "NEW={\"common.zz_n\": \"%d items in %s\"}; ${ADD_KEY}"
py "${EDIT_META}m[\"common.zz_n\"]={\"description\":\"Count.\",\"review\":\"standard\",\"placeholders\":[\"number of items\"]}${SAVE_META}"
expect 1 "placeholders shorter than the English verbs fail" "has 1 meaning(s) but the English text"

py "NEW={\"common.zz_n\": \"%d items in %s\"}; ${ADD_KEY}"
py "${EDIT_META}m[\"common.zz_n\"]={\"description\":\"Count.\",\"review\":\"standard\"}${SAVE_META}"
expect 1 "a described key with verbs but no placeholders fails" "add \`placeholders\`"

py "NEW={\"common.zz_n\": \"%d items (100%%) in %s\"}; ${ADD_KEY}"
py "${EDIT_META}m[\"common.zz_n\"]={\"description\":\"Count.\",\"review\":\"standard\",\"placeholders\":[\"number of items\",\"basket name\"]}${SAVE_META}"
expect 0 "matching placeholders pass and %% is not a placeholder"

py "NEW={\"common.zz_n\": \"%[1]d of %[2]d, %[1]d left\"}; ${ADD_KEY}"
py "${EDIT_META}m[\"common.zz_n\"]={\"description\":\"Count.\",\"review\":\"standard\",\"placeholders\":[\"done\",\"total\"]}${SAVE_META}"
expect 0 "a repeated positional verb names one placeholder"

# --- check 3: a meta key not in en.json ---
py "${EDIT_META}m[\"common.zz_gone\"]={\"description\":\"x\",\"review\":\"standard\"}${SAVE_META}"
expect 1 "a meta key missing from en.json fails" "common.zz_gone: not a key in"

# --- check 4: a placeholder change on an existing key ---
KEY_WITH_VERB="$(python3 -c 'import json,re;d=json.load(open("web/locales/en.json"));print(next(k for k,v in sorted(d.items()) if re.fullmatch(r"[^%]*%s[^%]*",v)))')"
py "import json; p=\"web/locales/en.json\"; d=json.load(open(p)); d[\"${KEY_WITH_VERB}\"]=d[\"${KEY_WITH_VERB}\"].replace(\"%s\",\"%d\"); json.dump(d, open(p,\"w\"), indent=2, ensure_ascii=False)"
expect 1 "changing an existing key's placeholder fails" "${KEY_WITH_VERB} changed its placeholders"
py "import json; p=\"web/locales/en.json\"; d=json.load(open(p)); d[\"${KEY_WITH_VERB}\"]=\"Reworded: \"+d[\"${KEY_WITH_VERB}\"]; json.dump(d, open(p,\"w\"), indent=2, ensure_ascii=False)"
expect 0 "rewording an existing key with the same placeholder passes"

# --- check 5: legal namespaces ---
py "NEW={\"receipt.zz_line\": \"Thank you\"}; ${ADD_KEY}"
py "${EDIT_META}m[\"receipt.zz_line\"]={\"description\":\"Footer.\",\"review\":\"standard\"}${SAVE_META}"
expect 1 "a legal-namespace key set to standard fails" "namespace 'receipt' is legal"

py "NEW={\"receipt.zz_line\": \"Thank you\"}; ${ADD_KEY}"
py "${EDIT_META}m[\"receipt.zz_line\"]={\"description\":\"Footer.\",\"review\":\"standard\",\"review_exception\":\"a greeting, no legal content (reviewed)\"}${SAVE_META}"
expect 0 "a legal-namespace key with a reviewed exception passes"

py "NEW={\"receipt.zz_line\": \"Thank you\"}; ${ADD_KEY}"
py "${EDIT_META}m[\"receipt.zz_line\"]={\"description\":\"Footer.\",\"review\":\"legal\"}${SAVE_META}"
expect 0 "a legal-namespace key set to legal passes"

py "${EDIT_META}m[\"common.save\"][\"review_exception\"]=\"x\"${SAVE_META}"
expect 1 "an exception on a non-legal key fails" "only applies to a legal-namespace key"

# --- check 6: bad field values ---
py "${EDIT_META}m[\"common.save\"][\"review\"]=\"maybe\"${SAVE_META}"
expect 1 "an invalid review value fails" "must be \"legal\" or \"standard\""
py "${EDIT_META}m[\"common.save\"][\"area\"]=\"nowhere\"${SAVE_META}"
expect 1 "an undefined area fails" "is not defined in"
py "${EDIT_META}m[\"common.save\"][\"max_length\"]=2${SAVE_META}"
expect 1 "English longer than its max_length fails" "over its own max_length 2"
py "${EDIT_META}m[\"common.save\"][\"descripton\"]=\"typo\"${SAVE_META}"
expect 1 "an unknown field fails" "unknown field 'descripton'"
py "${EDIT_META}m[\"common.save\"][\"plural\"]=\"\"${SAVE_META}"
expect 1 "an empty plural fails" "\`plural\` must name"
printf '{\n  "common.save": {"description": "a", "review": "standard"},\n  "common.save": {"description": "b", "review": "standard"}\n}\n' >web/i18n-meta/en.meta.json
expect 1 "a duplicated meta key fails" "is defined twice"

# --- check 7: area map ---
py 'import json; p="web/i18n-meta/areas.json"; a=json.load(open(p)); a["areas"]["sale"]["namespaces"].remove("pos"); json.dump(a, open(p,"w"), indent=2)'
expect 1 "an unmapped namespace fails" "namespace 'pos' (en.json) is in no area"
py 'import json; p="web/i18n-meta/areas.json"; a=json.load(open(p)); a["areas"]["common"]["namespaces"].append("pos"); json.dump(a, open(p,"w"), indent=2)'
expect 1 "a namespace in two areas fails" "namespace 'pos' is in both"

# --- check 8: baseline ---
echo "common.zz_never" >>scripts/ci/i18n-baseline/key-meta-baseline.txt
expect 1 "a stale baseline entry (key gone) fails" "common.zz_never is not in"
echo "common.save" >>scripts/ci/i18n-baseline/key-meta-baseline.txt
expect 1 "a baseline entry for a described key fails" "common.save now has a description"
# Growing the baseline with a new, undescribed key is the shrink-only breach.
py "NEW={\"common.zz_new\": \"New\"}; ${ADD_KEY}"
echo "common.zz_new" >>scripts/ci/i18n-baseline/key-meta-baseline.txt
expect 1 "a key added to the baseline fails (shrink-only)" "common.zz_new is new compared with base"
# Describing a baselined key and dropping its line is the intended shrink.
# A verb-free baselined key, picked without a pipe: `grep | grep -m1` on the
# 88 KB baseline SIGPIPEs the first grep under pipefail.
SHRINK_KEY="$(python3 -c 'import json;d=json.load(open("web/locales/en.json"));b=[l.strip() for l in open("scripts/ci/i18n-baseline/key-meta-baseline.txt") if l.strip() and not l.startswith("#")];print(next(k for k in b if k.startswith("pos.") and "%" not in d[k] and "{" not in d[k]))')"
grep -vxF "${SHRINK_KEY}" scripts/ci/i18n-baseline/key-meta-baseline.txt >"${OUT}.bl" && mv "${OUT}.bl" scripts/ci/i18n-baseline/key-meta-baseline.txt
expect 1 "dropping a baseline line without describing the key fails" "${SHRINK_KEY} has no metadata"
grep -vxF "${SHRINK_KEY}" scripts/ci/i18n-baseline/key-meta-baseline.txt >"${OUT}.bl" && mv "${OUT}.bl" scripts/ci/i18n-baseline/key-meta-baseline.txt
py "${EDIT_META}m[\"${SHRINK_KEY}\"]={\"description\":\"Described.\",\"review\":\"standard\"}${SAVE_META}"
expect 0 "describing a baselined key and dropping its line passes"

# --- base-ref fallback ---
if I18N_META_BASE=missing-ref bash "${GUARD}" >"${OUT}" 2>&1 && grep -q 'base ref missing-ref not available' "${OUT}"; then
  echo "✓ a missing base ref skips the base checks with a notice"
else
  echo "❌ FAIL: a missing base ref should pass with a skip notice" >&2
  cat "${OUT}" >&2
  FAILS=$((FAILS + 1))
fi

# --- the verb regex is guard-i18n.sh check 8's ---
regex_block() { sed -n '/^verb_re = re.compile(/,/^)/p' "$1"; }
if [ -n "$(regex_block scripts/ci/guard-i18n.sh)" ] &&
  [ "$(regex_block scripts/ci/guard-i18n.sh)" = "$(regex_block "${GUARD}")" ]; then
  echo "✓ verb regex matches guard-i18n.sh"
else
  echo "❌ FAIL: guard-i18n-meta.sh's verb_re drifted from guard-i18n.sh check 8's" >&2
  FAILS=$((FAILS + 1))
fi

if [ "${FAILS}" -ne 0 ]; then
  echo "❌ ${FAILS} guard-i18n-meta case(s) failed" >&2
  exit 1
fi
echo "✓ guard-i18n-meta self-test: all cases pass"
