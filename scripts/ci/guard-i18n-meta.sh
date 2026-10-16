#!/usr/bin/env bash
#
# Key metadata guard (ADR-0160 §2-§4, ut-docs#4362). Every key in
# web/locales/en.json carries metadata in web/i18n-meta/en.meta.json — the
# translator's context (Weblate's "explanation"), the meaning of each
# placeholder, an optional length limit, the review tier and the screen area.
# The metadata lives outside web/locales/ because NewI18nFS and guard-i18n.sh
# load every *.json there as a locale.
#
# Fails when:
#   1. a key has no `description` or no `review`, unless it is listed in the
#      shrink-only baseline scripts/ci/i18n-baseline/key-meta-baseline.txt;
#   2. a `placeholders` list doesn't match the English text (one meaning per
#      printf/template verb; `%%` is a literal, not a placeholder), or a
#      described key with verbs has no `placeholders`;
#   3. en.meta.json names a key that isn't in en.json;
#   4. an existing key's English verbs differ from the base ref's (a changed
#      meaning or placeholder set gets a NEW key — ADR-0160 §5: one bundle
#      serves every supported till line);
#   5. a key in a legal namespace (areas.json `legal_namespaces`: receipt,
#      fiscal, tax, invoice, legal, ...) is `review: standard` without a
#      reviewed exception — `"review_exception": "<reason>"` on that key's
#      entry, the JSON form of a same-line marker (JSON has no comments);
#   6. a field is unknown or has a bad value (`review` not legal|standard,
#      an `area` areas.json doesn't define, `max_length` not a positive
#      integer or shorter than the English text, an empty `plural`);
#      (whether `plural` names a real ICU plural argument of the English
#      value, and that every English plural carries `plural`, is checked by
#      the Go test TestRealLocaleBundle_PluralMetadataMatchesEnglish in
#      internal/config/i18n_tm_test.go, which `go test ./...` runs in CI —
#      ut-docs#4479);
#   7. areas.json doesn't map an en.json namespace to exactly one area;
#   8. a baseline entry is stale (key gone, or now described) or new
#      compared with the base ref's baseline (the baseline only shrinks).
#
# Env: I18N_META_BASE (default origin/main) — the ref checks 4 and 8 compare
# against. Unresolvable ref (a local clone without it) or no file on it yet
# → those checks are skipped with a notice; every other check always runs.
# CI fetches origin/main first.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"
BASE_REF="${I18N_META_BASE:-origin/main}"

EN="web/locales/en.json"
META="web/i18n-meta/en.meta.json"
AREAS="web/i18n-meta/areas.json"
BASELINE="scripts/ci/i18n-baseline/key-meta-baseline.txt"

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

if git rev-parse --verify --quiet "${BASE_REF}^{commit}" >/dev/null; then
  git show "${BASE_REF}:${EN}" >"${WORK}/base-en.json" 2>/dev/null || rm -f "${WORK}/base-en.json"
  git show "${BASE_REF}:${BASELINE}" >"${WORK}/base-baseline.txt" 2>/dev/null || rm -f "${WORK}/base-baseline.txt"
else
  echo "ℹ guard-i18n-meta: base ref ${BASE_REF} not available — placeholder-change and shrink-only checks skipped (CI fetches it)"
fi

python3 - "${EN}" "${META}" "${AREAS}" "${BASELINE}" "${WORK}" "${BASE_REF}" <<'PY'
import json, os, re, sys

en_path, meta_path, areas_path, baseline_path, work, base_ref = sys.argv[1:7]
errors = []

def err(msg):
    errors.append(msg)

def load(path, what):
    dupes = []
    def hook(pairs):
        seen = set()
        for k, _ in pairs:
            if k in seen:
                dupes.append(k)
            seen.add(k)
        return dict(pairs)
    try:
        with open(path, encoding="utf-8") as f:
            data = json.load(f, object_pairs_hook=hook)
    except (OSError, ValueError) as e:
        print(f"guard-i18n-meta: cannot read {what} {path}: {e}")
        sys.exit(1)
    for k in dupes:
        err(f"{path}: key {k!r} is defined twice (JSON keeps only the last)")
    return data

def load_lines(path):
    out = []
    with open(path, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if line and not line.startswith("#"):
                out.append(line)
    return out

# Same verb regex as guard-i18n.sh check 8 (ut-docs#1865); its self-test
# pins the two copies equal.
verb_re = re.compile(
    r'%%'
    r'|%\[(\d+)\]\d*(?:\.\d+)?[vTtbcdoOqxXUeEfFgGspw](?![A-Za-z])'
    r'|%\d*(?:\.\d+)?[vTtbcdoOqxXUeEfFgGspw](?![A-Za-z])'
    r'|\{\{[^{}]*\}\}'
    r'|\{\d+\}'
)

def verbs(s):
    # Ordered verb tokens, %% excluded (a literal percent sign).
    return [m.group(0) for m in verb_re.finditer(s) if m.group(0) != "%%"]

def placeholder_count(s):
    # One meaning per argument: positional printf verbs (%[1]s) that repeat
    # an index name one argument.
    n, seen = 0, set()
    for m in verb_re.finditer(s):
        tok = m.group(0)
        if tok == "%%":
            continue
        if m.group(1):
            if m.group(1) in seen:
                continue
            seen.add(m.group(1))
        n += 1
    return n

en = load(en_path, "base locale")
meta = load(meta_path, "key metadata")
areas_doc = load(areas_path, "area map")
baseline = load_lines(baseline_path) if os.path.exists(baseline_path) else []

# --- areas.json (check 7) ---
areas = areas_doc.get("areas")
legal_ns = areas_doc.get("legal_namespaces")
default_area = areas_doc.get("default_area")
if not isinstance(areas, dict) or not areas:
    err(f"{areas_path}: `areas` must be a non-empty object of area id -> {{description, namespaces}}")
    areas = {}
if not isinstance(legal_ns, list) or not legal_ns or not all(isinstance(n, str) and n for n in legal_ns):
    err(f"{areas_path}: `legal_namespaces` must be a non-empty list of namespace names")
    legal_ns = []
if default_area not in areas:
    err(f"{areas_path}: `default_area` {default_area!r} is not a defined area")
ns_area = {}
for area_id, spec in areas.items():
    if not isinstance(spec, dict) or not isinstance(spec.get("description"), str) or not spec["description"].strip():
        err(f"{areas_path}: area {area_id!r} needs a non-empty `description`")
        continue
    for ns in spec.get("namespaces", []):
        if ns in ns_area:
            err(f"{areas_path}: namespace {ns!r} is in both {ns_area[ns]!r} and {area_id!r} — an area is a permission boundary, so each namespace belongs to one")
        else:
            ns_area[ns] = area_id
unmapped = sorted({k.split(".")[0] for k in en} - ns_area.keys())
for ns in unmapped:
    err(f"{areas_path}: namespace {ns!r} (en.json) is in no area — add it to the area whose screens use it")

# --- en.meta.json entries (checks 2, 3, 5, 6) ---
FIELDS = {"description", "placeholders", "max_length", "review", "plural", "area", "review_exception"}
complete = set()
for key in sorted(meta):
    entry = meta[key]
    where = f"{meta_path}: {key}"
    if key not in en:
        err(f"{where}: not a key in {en_path} — remove the entry (a renamed key moves its metadata with it)")
        continue
    if not isinstance(entry, dict):
        err(f"{where}: entry must be an object")
        continue
    for field in sorted(entry.keys() - FIELDS):
        err(f"{where}: unknown field {field!r} (allowed: {', '.join(sorted(FIELDS))})")
    desc = entry.get("description")
    if desc is not None and (not isinstance(desc, str) or not desc.strip()):
        err(f"{where}: `description` must be a non-empty string")
    review = entry.get("review")
    if review is not None and review not in ("legal", "standard"):
        err(f"{where}: `review` must be \"legal\" or \"standard\", got {review!r}")
    area = entry.get("area")
    if area is not None and area not in areas:
        err(f"{where}: `area` {area!r} is not defined in {areas_path}")
    ml = entry.get("max_length")
    if ml is not None:
        if not isinstance(ml, int) or isinstance(ml, bool) or ml <= 0:
            err(f"{where}: `max_length` must be a positive integer")
        elif len(en[key]) > ml:
            err(f"{where}: the English text is {len(en[key])} characters, over its own max_length {ml}")
    plural = entry.get("plural")
    if plural is not None and (not isinstance(plural, str) or not plural.strip()):
        err(f"{where}: `plural` must name the ICU argument (a non-empty string)")
    want = placeholder_count(en[key])
    ph = entry.get("placeholders")
    if ph is not None:
        if not isinstance(ph, list) or not all(isinstance(p, str) and p.strip() for p in ph):
            err(f"{where}: `placeholders` must be a list of non-empty meanings, one per verb in order")
        elif len(ph) != want:
            err(f"{where}: `placeholders` has {len(ph)} meaning(s) but the English text {en[key]!r} has {want} placeholder(s) {verbs(en[key])}")
    is_complete = isinstance(desc, str) and desc.strip() and review in ("legal", "standard")
    if is_complete:
        complete.add(key)
        if want and ph is None:
            err(f"{where}: the English text has placeholder(s) {verbs(en[key])} — add `placeholders` with one meaning each")
    ns = key.split(".")[0]
    exc = entry.get("review_exception")
    if exc is not None and (not isinstance(exc, str) or not exc.strip()):
        err(f"{where}: `review_exception` must give the reviewed reason")
    elif ns in legal_ns and review == "standard" and exc is None:
        err(f"{where}: namespace {ns!r} is legal (ADR-0160 §4) — use \"review\": \"legal\", or add \"review_exception\": \"<reviewed reason>\"")
    elif exc is not None and not (ns in legal_ns and review == "standard"):
        err(f"{where}: `review_exception` only applies to a legal-namespace key set to \"standard\" — remove it")

# --- check 1 + baseline staleness (check 8, first half) ---
base_set = set(baseline)
seen = set()
for k in baseline:
    if k in seen:
        err(f"{baseline_path}: {k} is listed twice")
    seen.add(k)
    if k not in en:
        err(f"{baseline_path}: {k} is not in {en_path} — remove the stale entry")
    elif k in complete:
        err(f"{baseline_path}: {k} now has a description and review — remove it from the baseline")
missing = sorted(k for k in en if k not in complete and k not in base_set)
for k in missing:
    err(f"{en_path}: {k} has no metadata — add \"description\" and \"review\" (and \"placeholders\" for its verbs) to {meta_path}")

# --- base-ref checks (4, 8 second half) ---
base_en_path = os.path.join(work, "base-en.json")
if os.path.exists(base_en_path):
    with open(base_en_path, encoding="utf-8") as f:
        base_en = json.load(f)
    for k in sorted(en.keys() & base_en.keys()):
        if verbs(en[k]) != verbs(base_en[k]):
            err(f"{en_path}: {k} changed its placeholders from {verbs(base_en[k])} on {base_ref} to {verbs(en[k])} — a changed placeholder set needs a NEW key (ADR-0160 §5)")
base_bl_path = os.path.join(work, "base-baseline.txt")
if os.path.exists(base_bl_path):
    base_bl = set(load_lines(base_bl_path))
    for k in sorted(base_set - base_bl):
        err(f"{baseline_path}: {k} is new compared with {base_ref} — the baseline only shrinks; describe the key in {meta_path} instead")
elif os.path.exists(base_en_path):
    print(f"ℹ guard-i18n-meta: {base_ref} has no {baseline_path} yet — shrink-only check skipped")

if errors:
    print(f"❌ guard-i18n-meta: {len(errors)} problem(s) with key metadata (ADR-0160 §2-§4):")
    for e in errors:
        print("  " + e)
    sys.exit(1)
print(f"✓ i18n-meta guard: {len(complete)} described key(s), {len(baseline)} in the shrink-only baseline, {len(ns_area)} namespace(s) in {len(areas)} area(s)")
PY
