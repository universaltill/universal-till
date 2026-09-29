# Review: v0.30.9 release notes + phone-help label fixes

**Shipped:** `web/release-notes/{en,de,fa,ar,tr}/v0.30.9.md` for the 12
merges since v0.30.8 (phone sale screen, What's new, settings tiles,
permission groups, non-blocking dialogs, reorder list per location, the iOS
version stamp, the German tax prompt offline). Two label fixes in the new
"Selling on a phone" help section: de "Zahlen" and "Vor Ort / Zum
Mitnehmen", fa "صرف در محل", matching the till's own strings. docs-shots
manifest refreshed.

**Reviewer:** independent Fable subagent (text written by Opus 5.5). It
checked each translation against English and against the till's locale
strings, and found 11 label mismatches (de 4, fa 3, ar 2, tr 2, en 1 nit).
All fixed. It found the same two mismatches in the help section, fixed
here too. No PR numbers or card IDs; RTL arrows correct.

**Checked:** `guard-release-notes.sh v0.30.9`, `internal/releasenotes` tests
(owner-language rule), `guard-help-drift`, `guard-docs-shots`.

**Known, not changed here:** the German Sell help topic already uses
"Außer Haus" where the UI says "Zum Mitnehmen" (older text).

**Verdict:** safe to merge.
