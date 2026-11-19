#!/usr/bin/env bash
# Reject the reviewer skill's "WIP: pre-review snapshot" placeholder commit
# (ut-docs#5943).
#
# WHY: the reviewer skill snapshots work as a commit with that subject, then
# squashes with `git commit --amend --no-edit`, which keeps the placeholder
# message. It reached main in every repo (e.g. ut-cloud e0baf4f,
# universal-till 8643092). Nothing noticed, so CI now does.
#
# Input (stdin): one commit per line as `git log --format='%h %s' BASE..HEAD`
# (short SHA, one space, subject). No arguments.
# Match: subject only, case-insensitive, anchored: optional leading
# whitespace, `wip:`, then `pre-review snapshot` anywhere after it.
# Exit 0 when clean (an empty range passes: nothing to reject), 1 on any hit.
# Self-test: the _test.sh next to this file.
set -euo pipefail

WIP_RE='^[[:space:]]*wip:.*pre-review snapshot'
shopt -s nocasematch

hits=0
while IFS= read -r line || [ -n "$line" ]; do
  [ -n "$line" ] || continue
  sha="${line%% *}"
  subj="${line#"$sha"}"
  subj="${subj# }"
  if [[ $subj =~ $WIP_RE ]]; then
    echo "❌ $sha $subj" >&2
    hits=$((hits + 1))
  fi
done

if [ "$hits" -gt 0 ]; then
  cat >&2 <<'MSG'

These are the reviewer skill's placeholder commits ("WIP: pre-review snapshot");
that subject must never reach main (ut-docs#5943). Give the work a real message:
  git reset --soft "$(git merge-base origin/main HEAD)"
  git commit --reset-author -m "<area>: <what> (ut-docs#NNNN)"
or, if the placeholder is the only/last commit:
  git commit --amend --reset-author -m "<real message>"   # not --no-edit
then: git push --force-with-lease
MSG
  exit 1
fi
echo '✓ wip-commits guard: no "WIP: pre-review snapshot" placeholder commit in range'
