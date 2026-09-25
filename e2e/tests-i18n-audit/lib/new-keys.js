// PR-only "new key" carve-out for the German render audit (ut-docs#2805).
//
// CommonJS on purpose, same as tests-docs/lib.js — required both by
// audit-locale-render.spec.ts (Playwright transpiles specs to CJS) and by
// the plain `node --test` file next to it (new-keys_test.js).
//
// THE RULE: on a pull_request run, a finding whose visible text is exactly
// the en.json value of a key that is NEW in this PR (present in the PR's
// web/locales/en.json, absent from the base branch's) is deferred — reported
// as a notice, not a failure. Everything else still fails: hardcoded
// English, and any key that already existed on the base branch but is
// missing/untranslated in the de pack. On push to main and any non-PR run
// there is no base catalog, and nothing is deferred (strict).
//
// WHY: a PR that adds a visible en.json key deadlocked this audit.
//   - The pack can't lead: ut-plugin-language-de's check-key-drift.sh fails
//     any key core doesn't have yet ("orphan: FAIL always").
//   - The core couldn't pass: this audit checks the pack's main out, which
//     can't have the key yet, so T() renders the English fallback.
// The agreed order is "core merges, then the lane ships the pack PRs in the
// same cycle" (scrum-master SKILL, ut-docs#1857). The push-to-main run stays
// strict, so a pack that is still lagging after merge is still caught.
//
// Ambiguity stays strict: if a new key's value ALSO belongs to a key that
// exists on base, the rendered line can't tell us which key produced it, so
// that text is NOT deferred.

function trimmedValues(catalog, keys) {
  const out = new Set();
  for (const k of keys) {
    const v = typeof catalog[k] === 'string' ? catalog[k].trim() : '';
    if (v) out.add(v);
  }
  return out;
}

// The set of (trimmed) text values that belong ONLY to keys new in head
// relative to base. Empty when there is no base catalog.
function newKeyOnlyValues(headEn, baseEn) {
  if (!baseEn) return new Set();
  const headKeys = Object.keys(headEn);
  const newKeys = headKeys.filter((k) => !Object.prototype.hasOwnProperty.call(baseEn, k));
  const oldKeys = headKeys.filter((k) => Object.prototype.hasOwnProperty.call(baseEn, k));
  // Both the head and the base values of pre-existing keys count as
  // "already existed" — either could be what the pack has fallen behind on.
  const existing = new Set([...trimmedValues(headEn, oldKeys), ...trimmedValues(baseEn, Object.keys(baseEn))]);
  const out = new Set();
  for (const v of trimmedValues(headEn, newKeys)) if (!existing.has(v)) out.add(v);
  return out;
}

// Splits audit findings ({ text, ... }) into hard failures and deferred
// new-key findings. baseEn null/undefined => strict: nothing is deferred.
function partitionFindings(flags, headEn, baseEn) {
  const deferrable = newKeyOnlyValues(headEn, baseEn);
  const failures = [];
  const deferred = [];
  for (const f of flags) (deferrable.has(f.text) ? deferred : failures).push(f);
  return { failures, deferred };
}

module.exports = { newKeyOnlyValues, partitionFindings };
