#!/usr/bin/env bash
#
# Regression test for guard-appstore-no-code-fetch.sh (ADR-0194): runs the
# guard against fixture copies of the files it reads, in a fresh mktemp dir,
# and proves it rejects a removed gate call, a new un-gated install entry
# point, a network import in the bundled package, a malformed lock and
# BundledLockVerified set outside the bundled package; honours the reviewed
# exception marker; and passes on the real tree.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GUARD="${ROOT_DIR}/scripts/ci/guard-appstore-no-code-fetch.sh"
FAIL_COUNT=0

WORK="$(mktemp -d)"
# Invoked indirectly via `trap ... EXIT` (SC2317 false positive).
# shellcheck disable=SC2317
cleanup() { rm -rf -- "${WORK:?}"; }
trap cleanup EXIT

REPO="${WORK}/repo"

reset_tree() {
  rm -rf -- "${REPO:?}"
  mkdir -p "${REPO}"
  (
    cd "${ROOT_DIR}"
    {
      grep -E '^[a-z].*\.go\|' "${GUARD}" | cut -d'|' -f1
      echo internal/plugins/bundled/bundled.lock.json
      find internal/plugins/bundled -maxdepth 1 -name '*.go' ! -name '*_test.go'
      echo internal/pages/common/deps.go
      echo go.mod
      find internal/buildinfo -maxdepth 1 -name '*.go'
    } | LC_ALL=C sort -u | while IFS= read -r f; do
      mkdir -p "${REPO}/$(dirname "${f}")"
      cp "${f}" "${REPO}/${f}"
    done
  )
}

run_guard() { bash "${GUARD}" "${REPO}" >"${WORK}/out" 2>&1; }

expect_fail() { # <rule> <what>
  if run_guard; then
    echo "❌ FAIL: expected the guard to reject $2, but it passed" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  elif ! grep -qF "[$1]" "${WORK}/out"; then
    echo "❌ FAIL: the guard rejected $2, but not as [$1]" >&2
    cat "${WORK}/out" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  else
    echo "✓ guard rejected $2 [$1]"
  fi
}

expect_pass() {
  if run_guard; then
    echo "✓ guard passed $1"
  else
    echo "❌ FAIL: expected the guard to pass $1" >&2
    cat "${WORK}/out" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}

reset_tree
expect_pass "the copied real tree"

# 1. Every gate row: remove its token from the file -> rejected.
while IFS='|' read -r file token; do
  [ -n "${file}" ] || continue
  reset_tree
  python3 -I - "${REPO}/${file}" "${token}" <<'PY'
import sys
p, tok = sys.argv[1], sys.argv[2]
s = open(p).read()
open(p, "w").write(s.replace(tok, "REMOVED_GATE"))
PY
  expect_fail gate "${file} without ${token}"
done < <(grep -E '^[a-z].*\.go\|' "${GUARD}")

# 2. A new, un-gated install entry point.
reset_tree
printf 'package pages\n\nvar _ = plugins.MarketplaceInstallRequest{}\n' >"${REPO}/internal/pages/new_install.go"
expect_fail gate "a new file that builds a marketplace install request"
printf 'package pages\n\n// appstore-code-gate:allow reviewed, asset-only helper\nvar _ = plugins.MarketplaceInstallRequest{}\n' >"${REPO}/internal/pages/new_install.go"
expect_pass "a reviewed exception marker"
reset_tree
printf 'package pages\n\nvar _ = plugins.NewImporter(nil, "", nil)\n' >"${REPO}/internal/pages/new_import.go"
expect_fail gate "a new file that builds an Importer"

# 3. Network imports in the bundled package.
for imp in '"net/http"' '"net"' '"github.com/universaltill/universal-till/internal/plugins/marketplace"'; do
  reset_tree
  printf 'package bundled\n\nimport _ %s\n' "${imp}" >"${REPO}/internal/plugins/bundled/net.go"
  expect_fail network "bundled importing ${imp}"
done

# 4. Malformed lock.
lock="${REPO}/internal/plugins/bundled/bundled.lock.json"
mutate_lock() { # <python expr on e (first plugin)>
  reset_tree
  python3 -I - "${lock}" "$1" <<'PY'
import json, sys
p, expr = sys.argv[1], sys.argv[2]
d = json.load(open(p))
e = d["plugins"][0]
exec(expr)
json.dump(d, open(p, "w"))
PY
}
mutate_lock 'e["wasm_sha256"] = "abc"';                                expect_fail lock "a short sha256"
mutate_lock 'e["bundle_sha256"] = e["bundle_sha256"].upper()';         expect_fail lock "an upper-case sha256"
mutate_lock 'e["id"] = "com.example.thirdparty"';                      expect_fail lock "a third-party plugin id"
mutate_lock 'e["repo"] = "someone/ut-plugin-payment-x"';               expect_fail lock "a foreign repo"
mutate_lock 'e["release_tag"] = "v0.0.1"';                             expect_fail lock "a tag that is not v<version>"
mutate_lock 'd["plugins"] = []';                                       expect_fail lock "an empty plugin list"
reset_tree; echo '{' >"${lock}";                                       expect_fail lock "a lock that is not JSON"
reset_tree; rm -f -- "${lock}";                                        expect_fail lock "a missing lock"

# 5. BundledLockVerified outside the bundled package.
reset_tree
printf 'package pages\n\nvar _ = plugins.InstallOptions{BundledLockVerified: true}\n' >"${REPO}/internal/pages/flag.go"
expect_fail flag "BundledLockVerified set in the pages package"

# 6. Mutation: the predicate forced to `return true` keeps every token the
# grep looks for, so only the behaviour check (go test) can catch it.
reset_tree
python3 -I - "${REPO}/internal/buildinfo/channel.go" <<'PY'
import sys
p = sys.argv[1]
s = open(p).read()
old = "func (c InstallChannel) CodePluginInstallAllowed() bool { return c != ChannelAppStore }"
assert old in s
open(p, "w").write(s.replace(old, "func (c InstallChannel) CodePluginInstallAllowed() bool { return true }"))
PY
expect_fail behaviour "CodePluginInstallAllowed forced to return true"
reset_tree
python3 -I - "${REPO}/internal/buildinfo/channel.go" <<'PY'
import sys
p = sys.argv[1]
s = open(p).read()
old = "func (c InstallChannel) CodePluginInstallAllowed() bool { return c != ChannelAppStore }"
assert old in s
open(p, "w").write(s.replace(old, "func (c InstallChannel) CodePluginInstallAllowed() bool { return c != ChannelAppStore && c != ChannelDirect }"))
PY
expect_fail behaviour "CodePluginInstallAllowed refusing another channel"

# 7. Embeds by name; no fetched bundle committed; no production caller of a test seam.
reset_tree
sed -i.bak 's#^//go:embed embedded/#//go:embed all:embedded/#' "${REPO}/internal/plugins/bundled/embed_on.go" 2>/dev/null || true
rm -f -- "${REPO}/internal/plugins/bundled/embed_on.go.bak"
expect_fail embed "embed_on.go embedding a directory"
reset_tree
printf 'package pages\n\nvar _ = buildinfo.OverrideChannelForTest\n' >"${REPO}/internal/pages/seam.go"
expect_fail seam "a production caller of OverrideChannelForTest"
reset_tree
git -C "${REPO}" init -q
mkdir -p "${REPO}/internal/plugins/bundled/embedded/com.universaltill.payment-stripe/bin"
printf 'x' >"${REPO}/internal/plugins/bundled/embedded/com.universaltill.payment-stripe/bin/plugin.wasm"
git -C "${REPO}" add -f internal/plugins/bundled/embedded
expect_fail embed "a committed wasm bundle under the embed dir"

if [ "${FAIL_COUNT}" -ne 0 ]; then
  echo "${FAIL_COUNT} guard test(s) failed" >&2
  exit 1
fi
echo "✓ guard-appstore-no-code-fetch regression test passed"
