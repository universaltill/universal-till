# Code review — commit-attribution.yml doesn't catch a noreply ID belonging to a different real GitHub account (ut-docs#1373)

- **Date:** 2026-09-01
- **Branch:** `fix/1373-commit-attribution-id-check`
- **Reviewer:** independent reviewer (Opus subagent, this pipeline's
  `complexity:medium` review tier), fresh context, no prior exposure to the
  dev reasoning.
- **Verdict: SAFE TO MERGE**, after fixing two blockers and two should-fix
  findings the review round surfaced. First-pass diff was NOT safe as
  submitted — recorded honestly below rather than only showing the fixed
  state.
- **Cross-repo change.** This same fix (script content byte-identical
  modulo path) lands in `universal-till`, `ut-cloud`, `ut-infra`, `ut-docs`
  in one pipeline cycle — see each repo's own copy of this record. Applying
  to only the repo where the incident happened would leave three siblings
  carrying the pre-fix gap, per the card's own grooming note.

## What shipped

Real incident: commit `980c0287` (PR #592, merged 2026-08-28) used author
email `26383381+farshid3003@users.noreply.github.com`. GitHub matches
noreply addresses by the numeric ID, not the username text after the `+` —
and `26383381` belongs to a different real GitHub user (SanmayJoshi), not
farshidmirza (real ID `4035824`). `commit-attribution.yml`'s existing
`BANNED_RE` denylist had no way to catch this: the address is well-formed,
right domain, right shape — it just has the wrong ID inside it.

`commit-attribution.yml`'s inline bash loop is extracted into a standalone,
testable `scripts/(ci/)guard-commit-attribution.sh` (+ `_test.sh`, same
convention as every other guard in this repo), extended with:

- An **allowlist check** on numeric-ID-prefixed noreply addresses
  (`<id>+<username>@users.noreply.github.com`): the ID must be in
  `ALLOWED_IDS`, populated from this repo's real `list_repository_collaborators`
  result (`farshidmirza`/4035824, `pouria-teimouri`/35641125,
  `ugurozsahin`/3191028) — never guessed from git history alone.
- A **deliberately empty** `ALLOWED_LEGACY_USERNAMES` for the older
  no-ID-prefix noreply form (see Findings #2 below for why).
- Each `commit-attribution.yml` now runs the new `_test.sh` as a CI step,
  then pipes `git log --format='%H|%ae|%an' "$BASE..$HEAD"` into the guard
  instead of the old inline loop.

## Independent review — round 1 findings (both fixed, not deferred)

The reviewer ran all four repos' test suites for real, verified
`list_repository_collaborators` independently rather than trusting the
allowlist as given, and adversarially probed the regex/allowlist logic
directly (not just the checked-in test cases). Two blockers:

1. **Case-sensitivity + default-allow fallthrough let the exact incident
   address back in with a one-character variation.** The two recognizer
   regexes were the only gates; anything else in the
   `@users.noreply.github.com` domain fell through to `ok`. Verified
   against the real bad address: `26383381+farshid3003@Users.NoReply.GitHub.com`
   (case), `26383381+farshid.3003@users.noreply.github.com` (illegal
   username char), `26383381+a+b@...` (stray `+`), and
   `26383381+@...` (empty username) all passed the pre-fix guard.
   **Fixed:** lowercase the email once (`email_lc="${email,,}"`) before
   every match — `BANNED_RE` already used `grep -Ei`, so the script was
   internally inconsistent about case-sensitivity — and added a
   default-deny branch: anything matching `@users\.noreply\.github\.com$`
   that isn't recognized by either explicit shape is now rejected, not
   silently passed.
2. **`ALLOWED_LEGACY_USERNAMES=(farshid3003)` recreated the ticket's own
   bug class.** GitHub's legacy no-ID noreply form matches by the *current*
   login string. `farshid3003` is a retired login for the farshidmirza
   account (GitHub search finds no such user today; the workflow's own
   header comment records the rename — "claude 396 · farshid3003 207" is
   the OLD contributor label). An address using a freed login is credited
   to nobody today, and becomes reassignable to a stranger the moment
   anyone registers that login — exactly the #1373 condition. It also
   wasn't load-bearing: the workflow only ever checks a PR's *new* commits
   against its base, never full history, so this entry protected nothing
   already merged. **Fixed:** emptied the allowlist — every legacy
   no-ID-prefix noreply address is now rejected outright. If a genuinely
   live contributor is later confirmed still holding that exact login, an
   entry can be re-added with that confirmation on record.

Two should-fix findings, also fixed (not deferred — both are one-line,
directly load-bearing for the guard's own correctness):

3. **Field-order pipe injection.** `IFS='|' read -r sha name email` with
   `git log --format='%H|%an|%ae'` put author *name* before *email* — a
   `git config user.name 'Evil|Name'` paired with a banned email (e.g.
   `noreply@anthropic.com`) shifted the extra `|`-delimited byte into the
   unchecked `email` variable, letting a banned identity through
   undetected (reproduced and confirmed by the reviewer, rc=0 pre-fix).
   **Fixed:** swapped to `%H|%ae|%an` / `read -r sha email name` — email
   is now always the fully-bounded middle field, and name (whatever
   characters it contains) can only pollute its own unchecked field.
4. **No `pipefail` around the real `git log | guard.sh` pipe in the
   workflow's `run:` block**, plus the guard reporting "no commits to
   check" as a soft pass on an empty read. Actions' default shell has no
   `pipefail`, so an unreachable/rewritten base SHA made `git log` exit
   128 while the pipeline still reported success. **Fixed:**
   `set -euo pipefail` added to the workflow's check step, and the guard
   now treats zero commits read as a hard failure (a real PR always has
   ≥1 commit) rather than a silent "nothing to check."

All four fixes are covered by new/updated cases in
`guard-commit-attribution_test.sh` (18 assertions total per repo, all
passing for real — see Verification below), not just asserted fixed.

## Verification

| Check | Result |
|---|---|
| `guard-commit-attribution_test.sh` (this repo) | 18/18 pass |
| Same test, `ut-cloud` / `ut-infra` / `ut-docs` | 18/18 pass, each |
| `gofmt -l .` / `go build ./...` / `go vet ./...` | empty / pass / pass |
| `git status --short` (scope check, all 4 repos) | only the workflow file + the 2 new scripts |
| YAML validity, all 4 edited workflows | parses clean |
| Adversarial probes beyond the checked-in test file (mixed case, ID-prefix/suffix collision, malformed input, backtick/`$()` injection in author name, empty stdin) | all handled correctly, no bypass found on the post-fix version |

No Go/application code touched anywhere — this is a CI-only change, so the
full `go test ./...` / e2e suite / other CI guards are unaffected by
construction (confirmed via the scope check above, not assumed).

## Not fixed by this ticket (explicitly out of scope)

The existing misattributed commit `980c0287` was separately rewritten out
of `universal-till` history entirely (see ut-docs#1373's own issue
comments and the related ut-docs#1374/#1378 incident thread) — that is a
different action from this ticket, which is scoped to *prevention* only,
per the original issue body.

## Nice-to-have, not blocking (reviewer's own list, deferred)

- A dependabot/renovate-style `49699333+dependabot[bot]@users.noreply.github.com`
  form would now correctly hit the default-deny branch (no such bot is
  configured in any of the 4 repos today, so this is inert, not a
  regression) — worth an explicit allowlist entry if/when such a bot is
  ever added, rather than discovering the block on its first PR.
- Three numeric IDs hardcoded in 4 repo copies means 4 edits per new
  collaborator, with no automated check pinning the list to reality. The
  guard's own failure message points at exactly which file/array to edit,
  which mitigates the risk; a shared-source-of-truth follow-up is a
  reasonable future card, not required here.
