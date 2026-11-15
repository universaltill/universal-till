// PR-only carve-outs for the German render audit: "new key" (ut-docs#2805)
// and "pack lag" (ut-docs#5583).
//
// CommonJS on purpose, same as tests-docs/lib.js — required both by
// audit-locale-render.spec.ts (Playwright transpiles specs to CJS) and by
// the plain `node --test` file next to it (new-keys_test.js).
//
// THE RULE: on a pull_request run, a finding whose visible text is exactly
// the en.json value of a key that is NEW in this PR (present in the PR's
// web/locales/en.json, absent from the base branch's) is deferred — reported
// as a notice, not a failure. Hardcoded English still fails; a pre-existing
// key fails unless the pack-lag rule below defers it. On push to main and
// any non-PR run there is no base catalog, and nothing is deferred (strict).
//
// WHY: a PR that adds a visible en.json key deadlocked this audit.
//   - The pack can't lead: ut-plugin-language-de's check-key-drift.sh fails
//     any key core doesn't have yet ("orphan: FAIL always").
//   - The core couldn't pass: this audit checks the pack's main out, which
//     can't have the key yet, so T() renders the English fallback.
// Under ADR-0160 section 2 code merges with English only and Weblate fills
// the pack later; the push-to-main run defers nothing, so a pack still
// lagging after merge is reported there (report-only, never red main).
//
// Ambiguity stays strict: if a new key's value ALSO belongs to a key that
// exists on base, the rendered line can't tell us which key produced it, so
// that text is NOT deferred.
//
// SECOND CARVE-OUT, PACK LAG (ut-docs#5583): the same deadlock hits a
// PRE-EXISTING key. When the de pack merely lags on a key that already
// existed on base with the SAME English value (e.g. promotions.empty,
// reports.days.last_24h), an unrelated PR that renders it goes red though it
// changed nothing. ADR-0160 section 2 already accepts translation lag
// (Weblate fills the pack later), so on PRs such a finding is deferred too.
// A key is "lagging" when the pack's value for it is missing, non-string or
// blank, AND it is new in head or its trimmed English value equals base's.
// The lever a PR controls is the English value, so a changed value (head !=
// base) is never lagging. A pack value equal to the English (a cognate) is
// "translated" and stays strict (allowlist.json is the remedy). Pack data is
// external (checked out at main), so a PR cannot change a German value.
// Ambiguity stays strict: a text is deferred only if EVERY head key with that
// text is lagging and no base-only/changed base key also owns it.
// packDe null/undefined => this rule is off.

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

function hasOwn(o, k) {
  return Object.prototype.hasOwnProperty.call(o, k);
}

function isLagging(k, headEn, baseEn, packDe) {
  const pv = packDe[k];
  if (typeof pv === 'string' && pv.trim() !== '') return false;
  if (!hasOwn(baseEn, k)) return true; // new in head
  const h = typeof headEn[k] === 'string' ? headEn[k].trim() : '';
  const b = typeof baseEn[k] === 'string' ? baseEn[k].trim() : '';
  return h === b;
}

// Map<trimmed text, lagging keys[]> for texts owned only by lagging keys.
// Empty when there is no base catalog or no pack catalog.
function lagKeyOnlyValues(headEn, baseEn, packDe) {
  const out = new Map();
  if (!baseEn || !packDe) return out;
  const owners = new Map(); // text -> { keys: [], allLagging: bool }
  const own = (text, key, lagging) => {
    if (!text) return;
    const o = owners.get(text) || { keys: [], allLagging: true };
    o.keys.push(key);
    if (!lagging) o.allLagging = false;
    owners.set(text, o);
  };
  for (const k of Object.keys(headEn)) {
    if (typeof headEn[k] !== 'string') continue;
    own(headEn[k].trim(), k, isLagging(k, headEn, baseEn, packDe));
  }
  // A base value that head no longer carries unchanged (removed/changed key)
  // also owns its text and is never lagging.
  for (const k of Object.keys(baseEn)) {
    if (typeof baseEn[k] !== 'string') continue;
    const b = baseEn[k].trim();
    const h = hasOwn(headEn, k) && typeof headEn[k] === 'string' ? headEn[k].trim() : null;
    if (h !== b) own(b, k, false);
  }
  for (const [text, o] of owners) if (o.allLagging) out.set(text, o.keys);
  return out;
}

// Splits audit findings ({ text, ... }) into hard failures and deferred ones.
// deferredNew: text of a key new in this PR. deferredLag: text of keys the pack
// lags on (each tagged with `keys`). `deferred` is both. baseEn null/undefined
// => strict: nothing is deferred. packDe omitted => only the new-key rule.
function partitionFindings(flags, headEn, baseEn, packDe) {
  const newOnly = newKeyOnlyValues(headEn, baseEn);
  const lag = lagKeyOnlyValues(headEn, baseEn, packDe);
  const failures = [];
  const deferredNew = [];
  const deferredLag = [];
  for (const f of flags) {
    if (newOnly.has(f.text)) deferredNew.push(f);
    else if (lag.has(f.text)) deferredLag.push({ ...f, keys: lag.get(f.text) });
    else failures.push(f);
  }
  return { failures, deferred: [...deferredNew, ...deferredLag], deferredNew, deferredLag };
}

module.exports = { newKeyOnlyValues, lagKeyOnlyValues, partitionFindings };
