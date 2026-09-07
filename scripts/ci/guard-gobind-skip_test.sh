#!/usr/bin/env bash
#
# Regression test for guard-gobind-skip.sh (ut-docs#1735): proves the guard
# actually rejects the skip-comment shape gobind emits, and does not
# false-positive on unrelated prose that happens to contain the word
# "skip" — rather than merely passing on whatever the tree currently looks
# like. A source-level guard that has never been shown to fail is
# indistinguishable from one whose grep silently stopped matching (same
# reasoning as guard-android-external-links_test.sh and
# guard-htmx-loaded.sh).
#
# Unlike guard-android-external-links_test.sh, this guard's fixture mode
# takes a directory argument rather than mutating a real committed source
# file (guard-gobind-skip.sh has no fixed "real" .java source to plant
# into — its real-CI mode generates one fresh via gobind each run). So each
# case here builds its own small throwaway fixture tree under a fresh
# mktemp -d, exercises the guard's fixture mode (`guard-gobind-skip.sh
# <dir>`) against it, and cleans up via trap. This exercises exactly the
# scanning logic the real no-args mode also uses (scan_dir), without
# needing gobind or network access.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

GUARD="scripts/ci/guard-gobind-skip.sh"
FAIL_COUNT=0

WORKDIRS=()
cleanup() {
  local status=$?
  for d in "${WORKDIRS[@]:-}"; do
    [ -n "$d" ] && rm -rf "$d"
  done
  exit "$status"
}
trap cleanup EXIT

# new_fixture: makes a fresh empty dir, remembers it for cleanup, and
# prints its path.
new_fixture() {
  local d
  d="$(mktemp -d)"
  WORKDIRS+=("$d")
  printf '%s' "$d"
}

expect_pass() {
  local dir="$1" label="$2"
  if bash "${GUARD}" "$dir" >/dev/null 2>&1; then
    echo "✓ guard correctly accepted ${label}"
  else
    echo "❌ FAIL: expected the guard to accept ${label}, but it failed" >&2
    bash "${GUARD}" "$dir" >&2 || true
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}

# expect_fail_naming ARGS: asserts the guard exits non-zero AND that its
# output actually names the given function/method — a guard that merely
# fails without saying *what* was dropped is not much better than the
# silent status quo this card exists to fix.
expect_fail_naming() {
  local dir="$1" want_name="$2" label="$3"
  local out
  if out="$(bash "${GUARD}" "$dir" 2>&1)"; then
    echo "❌ FAIL: expected the guard to reject ${label}, but it passed" >&2
    echo "$out" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
    return
  fi
  if printf '%s\n' "$out" | grep -qF "$want_name"; then
    echo "✓ guard correctly rejected ${label} and named ${want_name}"
  else
    echo "❌ FAIL: guard rejected ${label} but its output never named ${want_name}" >&2
    echo "$out" >&2
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}

# 1. A clean tree: no skip comments anywhere -> guard passes.
clean_dir="$(new_fixture)"
mkdir -p "${clean_dir}/com/universaltill/pos"
cat > "${clean_dir}/com/universaltill/pos/Mobile.java" <<'EOF'
package com.universaltill.pos;

public final class Mobile {
    public static native String start(String dataDir);
    public static native void stop();
    public static native boolean isRunning();
}
EOF
expect_pass "$clean_dir" "a clean generated tree with no skip comments"

# 2. Exactly gobind's real skip-comment wording, top-level file -> guard
#    fails and names the skipped function.
top_dir="$(new_fixture)"
mkdir -p "${top_dir}/com/universaltill/pos"
cat > "${top_dir}/com/universaltill/pos/Mobile.java" <<'EOF'
package com.universaltill.pos;

// skipped function SetBluetoothBridge with unsupported parameter or return types

public final class Mobile {
}
EOF
expect_fail_naming "$top_dir" "SetBluetoothBridge" \
  "a top-level file with gobind's real skip-comment wording"

# 3. A skip comment nested several directories deep -> still caught (the
#    scan isn't shallow / isn't only checking one well-known file).
deep_dir="$(new_fixture)"
mkdir -p "${deep_dir}/x/y/z/w/deep/nested/pkg"
cat > "${deep_dir}/x/y/z/w/deep/nested/pkg/Obscure.java" <<'EOF'
package deep.nested.pkg;

// skipped method ConfigureWidget with unsupported parameter or return types
public final class Obscure {
}
EOF
expect_fail_naming "$deep_dir" "ConfigureWidget" \
  "a skip comment several directories deep"

# 4. A doc comment whose prose mentions "skip" in an unrelated sense (not
#    gobind's exact phrase as a real // comment) -> guard still passes.
#    This mirrors the real BluetoothBridge.java gobind generates from
#    mobile/mobile.go's own doc comment, which documents this very bug and
#    so contains the string 'skipped function SetBluetoothBridge with
#    unsupported parameter or return types' inside Javadoc prose, never as
#    an actual `//` skip comment. A bare substring match on "skip" would
#    false-positive on that file forever; anchoring on comment-start plus
#    the full phrase must not.
docprose_dir="$(new_fixture)"
mkdir -p "${docprose_dir}/com/universaltill/pos"
cat > "${docprose_dir}/com/universaltill/pos/BluetoothBridge.java" <<'EOF'
package com.universaltill.pos;

/**
 * BluetoothBridge is the gomobile-bind-visible mirror of
 * bluetooth.AndroidBridge. A parameter type from an unbound package is
 * silently dropped — verified empirically: gobind emits "skipped function
 * SetBluetoothBridge with unsupported parameter or return types" and
 * still exits 0, so every gate stayed green while the method Kotlin
 * needed simply didn't exist. We also skip the flaky retry test on CI for
 * unrelated reasons — see ut-docs#999.
 */
public interface BluetoothBridge {
    String listDevices() throws Exception;
}
EOF
expect_pass "$docprose_dir" \
  "a doc comment whose prose mentions 'skip'/'skipped' without gobind's real // skip-comment shape"

# 5. The real generated shape of case 4, not a stand-in: gobind's actual
#    Javadoc output wraps a Go doc comment's continuation lines FLUSH LEFT
#    (no ` * ` prefix) with quotes HTML-escaped, so the full phrase can land
#    on one unprefixed line — verified against a real `gobind -lang=java`
#    run during this guard's own review. Case 4 above never puts the whole
#    phrase on a single line, so it can't prove the `//`-anchor requirement
#    (as opposed to the phrase match) is actually doing anything; this case
#    can and does — dropping the anchor from SKIP_PATTERN makes this fixture
#    fail where it must pass.
realdoc_dir="$(new_fixture)"
mkdir -p "${realdoc_dir}/mobile"
cat > "${realdoc_dir}/mobile/BluetoothBridge.java" <<'EOF'
package mobile;

/**
 * BluetoothBridge is the gomobile-bind-visible mirror of bluetooth.AndroidBridge.
empirically: gobind emits &#34;skipped function SetBluetoothBridge with unsupported parameter or return types&#34; and still exits 0, so every gate stayed green
 */
public interface BluetoothBridge {
    String listDevices() throws Exception;
}
EOF
expect_pass "$realdoc_dir" \
  "gobind's real flush-left Javadoc continuation line carrying the full phrase, unprefixed by //"

# 6. A genuine `//` comment that uses the word "skip" but not gobind's
#    distinctive phrase -> guard still passes. Proves the phrase-specificity
#    half of SKIP_PATTERN is load-bearing, not just the `//` anchor —
#    weakening the pattern to a bare `^\s*//.*skip` would fail this case.
looseword_dir="$(new_fixture)"
mkdir -p "${looseword_dir}/mobile"
cat > "${looseword_dir}/mobile/Mobile.java" <<'EOF'
package mobile;

// we skip flaky tests here, see ut-docs#999
public final class Mobile {
}
EOF
expect_pass "$looseword_dir" \
  "a genuine // comment mentioning 'skip' without gobind's distinctive phrase"

# 7. gobind's field/variable/const skip wording ("unsupported type: ...")
#    is a different phrase shape than the function/method/constructor one
#    ("... with unsupported parameter or return types") every other case
#    above uses -> must still be caught, and named. Proves SKIP_PATTERN
#    covers the whole family, not just the one shape the other cases happen
#    to share.
field_dir="$(new_fixture)"
mkdir -p "${field_dir}/mobile"
cat > "${field_dir}/mobile/Mobile.java" <<'EOF'
package mobile;

// skipped field Foo.Bar with unsupported type: chan int
public final class Mobile {
}
EOF
expect_fail_naming "$field_dir" "Foo.Bar" \
  "gobind's field-skip wording ('unsupported type: ...'), a different shape than the function-skip one"

if [ "${FAIL_COUNT}" -ne 0 ]; then
  echo "❌ guard-gobind-skip_test: ${FAIL_COUNT} assertion(s) failed" >&2
  exit 1
fi
echo "✓ guard-gobind-skip_test: all assertions passed"
