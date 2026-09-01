#!/usr/bin/env bash
#
# Guard: every commit AUTHOR must be attributable to a real, known
# contributor — never an AI-tool identity, an address linked to nobody, or a
# well-formed users.noreply.github.com address whose numeric ID belongs to a
# DIFFERENT real GitHub account than the name/username it's paired with.
#
# WHY THIS EXISTS, part 1 (AI-tool / unattributable addresses):
# GitHub's contributor graph counts *commit authors on the default branch*,
# not PR authors. On 2026-08-14 an audit of this repo found 617 of ~1,242
# commits on main authored `noreply@universaltill.com` — an address verified
# on no GitHub account, so credited to nobody — and 396 authored
# `Claude <noreply@anthropic.com>`. The public contributors list read
# "claude 396 · farshid3003 207", i.e. it said an AI tool wrote more of this
# product than the person who built it. Separately a human contributor with
# two merged PRs had 0 contributions in every repo in the org, because every
# commit inside those PRs was authored as Claude. Root cause was local git
# config: per-repo `user.email` overrides pointing at AI tool identities.
# Nothing in CI noticed for over a thousand commits. See ut-docs#732.
#
# WHY THIS EXISTS, part 2 (wrong-but-well-formed noreply ID, ut-docs#1373):
# Commit 980c0287 (PR #592, merged 2026-08-28) used author email
# `26383381+farshid3003@users.noreply.github.com`. GitHub's noreply-email
# matching goes by the NUMERIC ID, not the username text after the '+' — and
# 26383381 belongs to a different real GitHub user (SanmayJoshi), not to
# farshidmirza (real ID 4035824). GitHub silently credited that commit, and a
# "contributor" mark on the repo, to someone who never touched this project.
# The address LOOKS exactly like a correct one (right shape, right domain),
# so nothing before this guard could tell the difference. Part 1's BANNED_RE
# is a fixed denylist; this is an allowlist check on top of it, because the
# bad case here isn't a known-bad string — it's a well-formed one with the
# wrong ID inside it.
#
# Reads one commit per line from stdin, format 'sha|author email|author
# name' — the same shape `git log --format='%H|%ae|%an'` produces (see
# commit-attribution.yml, which pipes that straight in). EMAIL BEFORE NAME,
# deliberately: `IFS='|' read` puts everything past the last split field into
# the final variable, so a name containing a literal '|' (a valid git commit
# author name) can't smuggle bytes into the email field and dodge every
# check below — it can only pollute its own (unchecked) field. See
# guard-commit-attribution_test.sh's pipe-in-name case. Kept as a
# standalone, testable script — see guard-commit-attribution_test.sh —
# rather than inline workflow YAML, same convention as every other guard
# under scripts/ci/.
set -euo pipefail

# Known real GitHub accounts allowed to author commits here, as
# "<numeric-id>  # <login, for humans reading this list only>". The ID is
# what's actually checked. Get a contributor's real numeric ID from
# `gh api users/<login> --jq .id` or repo Settings → Collaborators — never
# from a noreply address alone, which is exactly the class of mistake this
# guard exists to catch.
ALLOWED_IDS=(
  4035824   # farshidmirza (Farshid Mirza — product owner / pipeline identity)
  35641125  # pouria-teimouri
  3191028   # ugurozsahin
)

# Older-style GitHub noreply addresses carry no numeric-ID prefix
# (<username>@users.noreply.github.com) — GitHub matches these by the LOGIN
# STRING, which is exactly the ut-docs#1373 bug class in a different
# disguise: if the login is ever renamed or the account deleted, that exact
# address becomes either unattributable (nobody holds it) or reassignable to
# a stranger who registers the freed login — and either way this guard
# would have no way to tell. Deliberately empty: this repo's real
# contributors all use the numeric-ID-prefixed form today (verified against
# `list_repository_collaborators`; "farshid3003" is a RETIRED login for the
# farshidmirza account — GitHub search finds no such user — so allowing it
# would silently recreate #1373). Add an entry here only after confirming
# the login is still actively held by the named person, and re-confirm
# periodically — usernames can be renamed away at any time.
ALLOWED_LEGACY_USERNAMES=()

# Matched case-insensitively via $email_lc below — GitHub email matching
# (and the domain/local-part grammar generally) is not case-sensitive, so a
# denylist or allowlist that only matched exact-case would let
# `noreply@Anthropic.com` or `26383381+X@Users.NoReply.GitHub.Com` sail
# through untouched. lower() once, match everywhere on the lowered string.
BANNED_RE='^(noreply@anthropic\.com|codex@users\.noreply\.github\.com|noreply@universaltill\.com)$'
ID_PREFIXED_RE='^([0-9]+)\+[a-z0-9-]+@users\.noreply\.github\.com$'
LEGACY_RE='^([a-z0-9-]+)@users\.noreply\.github\.com$'
NOREPLY_DOMAIN_RE='@users\.noreply\.github\.com$'

is_allowed_id() {
  local id="$1" a
  for a in "${ALLOWED_IDS[@]}"; do [ "$id" = "$a" ] && return 0; done
  return 1
}

is_allowed_legacy_username() {
  local u="$1" a
  for a in "${ALLOWED_LEGACY_USERNAMES[@]}"; do [ "$u" = "$a" ] && return 0; done
  return 1
}

bad=0
seen=0
while IFS='|' read -r sha email name; do
  [ -n "$sha" ] || continue
  seen=$((seen + 1))
  email_lc="${email,,}"

  if printf '%s' "$email_lc" | grep -Eq "$BANNED_RE"; then
    echo "::error::${sha:0:9} is authored by '${name} <${email}>' — AI-tool or unattributable identity"
    bad=1
  elif [[ "$email_lc" =~ $ID_PREFIXED_RE ]]; then
    id="${BASH_REMATCH[1]}"
    if is_allowed_id "$id"; then
      echo "ok  ${sha:0:9}  ${name} <${email}>"
    else
      echo "::error::${sha:0:9} is authored by '${name} <${email}>' — numeric ID ${id} is not a known contributor ID (see ALLOWED_IDS in scripts/ci/guard-commit-attribution.sh)"
      bad=1
    fi
  elif [[ "$email_lc" =~ $LEGACY_RE ]]; then
    uname="${BASH_REMATCH[1]}"
    if is_allowed_legacy_username "$uname"; then
      echo "ok  ${sha:0:9}  ${name} <${email}>"
    else
      echo "::error::${sha:0:9} is authored by '${name} <${email}>' — legacy noreply username '${uname}' is not a known contributor (see ALLOWED_LEGACY_USERNAMES in scripts/ci/guard-commit-attribution.sh)"
      bad=1
    fi
  elif printf '%s' "$email_lc" | grep -Eq "$NOREPLY_DOMAIN_RE"; then
    # Anything ending in @users.noreply.github.com that matched NEITHER
    # recognized shape above — e.g. a stray extra '+', a character GitHub
    # usernames can't contain, a truncated/malformed address. DEFAULT-DENY
    # here on purpose: this is exactly how ut-docs#1373's own address would
    # have slipped past an "unrecognized shape passes by default" guard
    # with a one-character variation. A real GitHub noreply address always
    # matches one of the two patterns above; anything else is suspect.
    echo "::error::${sha:0:9} is authored by '${name} <${email}>' — unrecognized users.noreply.github.com address shape (matches neither the ID-prefixed nor legacy form)"
    bad=1
  else
    echo "ok  ${sha:0:9}  ${name} <${email}>"
  fi
done

if [ "$seen" -eq 0 ]; then
  # A real PR always has at least one commit. Zero here almost always means
  # the git-log invocation upstream silently produced nothing (e.g. an
  # unreachable/rewritten base SHA) rather than that there was genuinely
  # nothing to check — fail closed instead of reporting a clean bill for a
  # check that never actually ran.
  echo "::error::guard-commit-attribution: no commits were read from stdin — the upstream git log likely failed silently (unreachable base/head SHA?); treating as a check failure rather than a pass"
  exit 1
fi

if [ "$bad" -ne 0 ]; then
  cat <<'MSG'

────────────────────────────────────────────────────────────────
Commit attribution check failed.

At least one commit on this PR is authored by an AI tool identity, by an
address that is not linked to any GitHub account, or by a
users.noreply.github.com address whose numeric ID does not match any known
contributor — meaning it's either a typo/stale copy-paste of someone else's
ID, or genuinely belongs to a different GitHub account than the name next
to it.

Fix your git identity, then re-author the commits:

    git config --global user.name  "Your Name"
    git config --global user.email "you@example.com"

Use an address that is VERIFIED on your GitHub account, or your own
<your-numeric-id>+<username>@users.noreply.github.com address — find your
real numeric ID at https://api.github.com/users/<your-username> (the "id"
field), never by copying an ID from an old commit or another account. Also
check for a per-repo override, which silently wins over the global one:

    git config --local --list | grep user\.

Then rewrite this branch's commits with the corrected identity:

    git rebase -r --exec 'git commit --amend --no-edit --reset-author' origin/main
    git push --force-with-lease

Keep crediting the AI tool — as a trailer, not as the author:

    Co-Authored-By: Claude <noreply@anthropic.com>

If this IS a new, legitimate contributor's own noreply address, add their
numeric ID to ALLOWED_IDS in scripts/ci/guard-commit-attribution.sh.
────────────────────────────────────────────────────────────────
MSG
  exit 1
fi

echo "All commit authors are attributable."
