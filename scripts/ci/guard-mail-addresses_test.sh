#!/usr/bin/env bash
# Self-test for guard-mail-addresses.sh (ut-docs#4229). Builds throwaway git
# repos in a fresh mktemp -d and runs the guard against each; never touches the
# real tree. This file is byte-identical in ut-docs, universal-till, ut-cloud
# and ut-infra (it finds the guard next to itself) -- see the guard's header.
# Bad addresses are assembled from $D so this file never contains one itself.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GUARD="${HERE}/guard-mail-addresses.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT

D="universaltill.com"
U="UniversalTill" # mixed-case fixtures: "x@${U}.COM"
failures=0
n=0
OUT=""
RC=0

# new_repo: fresh fixture repo in ${TMP}/rN; sets REPO.
new_repo() {
  n=$((n + 1))
  REPO="${TMP}/r${n}"
  mkdir -p "${REPO}"
  git -C "${REPO}" init -q
  git -C "${REPO}" config user.name "fixture"
  git -C "${REPO}" config user.email "fixture@example.invalid"
}

# commit_all: track everything written so far.
commit_all() {
  git -C "${REPO}" add -A
  git -C "${REPO}" commit -q -m fixture
}

run_guard() {
  RC=0
  OUT="$(bash "${GUARD}" "$@" 2>&1)" || RC=$?
}

show_out() {
  local l
  while IFS= read -r l; do echo "    ${l}"; done <<<"${OUT}"
}

expect() { # <name> <pass|fail>
  if [[ "$2" == pass && ${RC} -ne 0 ]] || [[ "$2" == fail && ${RC} -eq 0 ]]; then
    echo "FAIL: $1 -- expected $2, guard exited ${RC}"
    show_out
    failures=$((failures + 1))
  fi
}

expect_out() { # <name> <fixed string that must appear>
  if ! grep -qF -- "$2" <<<"${OUT}"; then
    echo "FAIL: $1 -- output lacks '$2'"
    show_out
    failures=$((failures + 1))
  fi
}

expect_no_out() { # <name> <fixed string that must NOT appear>
  if grep -qF -- "$2" <<<"${OUT}"; then
    echo "FAIL: $1 -- output unexpectedly has '$2'"
    show_out
    failures=$((failures + 1))
  fi
}

# 1. every allow-listed local part passes
new_repo
{
  for p in support hello security partners plugins docs licensing notify homelab noreply no-reply admin admin1 admin2 developer; do
    echo "mail ${p}@${D}"
  done
} >"${REPO}/ok.txt"
commit_all
run_guard "${REPO}"
expect "allowed addresses" pass

# 2. unknown address fails and names file:line
new_repo
printf 'line one\nwrite to bogus@%s now\n' "${D}" >"${REPO}/a.md"
commit_all
run_guard "${REPO}"
expect "unknown address" fail
expect_out "unknown address" "a.md:2: bogus@${D}"

# 3. mixed case: allowed passes, unknown fails
new_repo
echo "Support@${U}.COM" >"${REPO}/a.txt"
commit_all
run_guard "${REPO}"
expect "mixed-case allowed" pass
new_repo
echo "Bogus@${U}.COM" >"${REPO}/a.txt"
commit_all
run_guard "${REPO}"
expect "mixed-case unknown" fail
expect_out "mixed-case unknown" "a.txt:1: Bogus@${U}.COM"

# 4. subdomain addresses and look-alike domains are not mailboxes
new_repo
{
  echo "x@cloud.${D}"
  echo "admin@universaltill.id.${D}"
  echo "x@universaltill.community"
  echo "x@${D}.au and y@${D}.evil.example"
  echo "x@${D}-staging.example"
} >"${REPO}/a.txt"
commit_all
run_guard "${REPO}"
expect "subdomain / look-alike ignored" pass

# 5. address followed by punctuation is still matched
new_repo
echo "(mail bogus@${D}.)" >"${REPO}/a.txt"
commit_all
run_guard "${REPO}"
expect "trailing punctuation" fail
expect_out "trailing punctuation" "a.txt:1: bogus@${D}"
expect_no_out "trailing punctuation" "bogus@${D}."

# 6. mail-address:allow exempts the line, but only that line
new_repo
printf 'bogus@%s # mail-address:allow reviewed fixture\nbogus@%s\n' "${D}" "${D}" >"${REPO}/a.txt"
commit_all
run_guard "${REPO}"
expect "allow marker" fail
expect_no_out "allow marker" "a.txt:1:"
expect_out "allow marker" "a.txt:2:"
new_repo
printf 'bogus@%s # mail-address:allow reviewed fixture\n' "${D}" >"${REPO}/a.txt"
commit_all
run_guard "${REPO}"
expect "allow marker alone" pass

# 7. untracked files are ignored
new_repo
echo "ok" >"${REPO}/a.txt"
commit_all
echo "bogus@${D}" >"${REPO}/untracked.txt"
run_guard "${REPO}"
expect "untracked ignored" pass

# 8. two offenders on one line are both reported
new_repo
echo "one@${D} and two@${D}" >"${REPO}/a.txt"
commit_all
run_guard "${REPO}"
expect "two offenders" fail
expect_out "two offenders" "a.txt:1: one@${D}"
expect_out "two offenders" "a.txt:1: two@${D}"

# 9. a real offender next to an allowed one on one line
new_repo
echo "support@${D} bogus@${D}" >"${REPO}/a.txt"
commit_all
run_guard "${REPO}"
expect "mixed line" fail
expect_no_out "mixed line" "support@"

# 10. a ROOT that is missing or not a git repo is an error, not a pass
run_guard "${TMP}/does-not-exist"
expect "missing root" fail
mkdir -p "${TMP}/plain"
run_guard "${TMP}/plain"
expect "non-git root" fail

# 11. binary files are skipped
new_repo
printf 'bogus@%s\000\001' "${D}" >"${REPO}/a.bin"
commit_all
run_guard "${REPO}"
expect "binary skipped" pass

# 12. default ROOT is the git toplevel of the script: the real repo passes
RC=0
OUT="$(bash "${GUARD}" 2>&1)" || RC=$?
expect "real repo" pass

if [[ ${failures} -ne 0 ]]; then
  echo "guard-mail-addresses_test: ${failures} failure(s)"
  exit 1
fi
echo "guard-mail-addresses_test: OK"
