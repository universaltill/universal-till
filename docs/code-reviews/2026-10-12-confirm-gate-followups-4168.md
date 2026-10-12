# Review: AI confirm gate follow-ups, items 3–6 (ut-docs#4168)

**Shipped.**
- Item 3: `action_propose` returns -2 (audited, with a fixed reason; the
  plugin-supplied name is never logged) for an unknown action name, the
  same as a missing grant, so a plugin without the grants can't enumerate
  names. It used to return -1.
- Item 4: two new tests.
  - A cross-site POST is refused without consuming the proposal; a
    same-origin confirm afterwards still works.
  - 8 concurrent confirms apply exactly once: the 7 losers get "no longer
    valid" and there is one decision audit row.
- Item 6: a comment that `UT_AUTH=off` stamps no operator, so agent-write
  e2e tests must sign in for real.
- Item 5 (the ADR wording) is in the ut-docs PR.

**TDD and mutation checks.**
- The unknown-name tests failed first with -1, then passed.
- Removing the Sec-Fetch-Site check fails the cross-site test.
- Splitting `Store.Take` into separate lock sections fails the concurrency
  test. A first version of that test passed under this mutation (the stale
  check also answers 409), so it now asserts "no longer valid" and a
  single audit row.

**Reviewer.** Independent Fable subagent.
- Medium: the ADR claimed every `POST /ui/*` route checks Sec-Fetch-Site,
  but only the confirm route does. **Fixed**: SameSite=Lax is shared, the
  header check is this route's own.
- Low: "can't enumerate" was too absolute (a fully granted plugin can still
  tell names apart, but it may use those actions anyway). **Fixed** in
  wording.
- Info: the audit is safe, -2 is consistent across code, tests and docs,
  and the tests can't flake on SQLite (Take is in memory).

**Still open on #4168:** item 1 (satellite refusal at propose), item 2
(expiry audit timer), item 7 (de/tr help).

Verdict: safe to merge.
