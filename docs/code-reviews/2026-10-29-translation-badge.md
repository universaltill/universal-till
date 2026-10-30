# Review: Weblate translation badge in the README

Scope: one README line, a Weblate status badge linking to
https://translate.universaltill.com/engage/universal-till/ so people can find
and join the translation project.

Checked: the badge URL (`/widget/universal-till/svg-badge.svg`, project-wide,
not a single language) and the engage page both return 200 over HTTPS. The
badge is a public image; it exposes only translation percentages, which the
public engage page already shows. No code or secret is touched.

Verdict: safe to merge. Deferred: none.
