# Review: the PAN redactor no longer eats the daily backup's snapshot name (ut-docs#3999)

Branch `fix/3999-pan-redact-backup-stamp` (mirrored in ut-cloud on the same
branch name). Author: Opus 5.5 (lane:cloud-24, complexity:easy, written
inline). Independent review: Fable, fresh context, separate worktrees.

## What shipped

- Daily snapshots are named `unitill-pos-YYYYMMDD-HHMMSS.db`
  (`internal/db/backup.go` `backupTimeLayout`). The stamp is a 14-digit
  dash-grouped run. About 1 stamp in 10 passes Luhn with a 2-series IIN, so
  `redactPANs` turned it into `unitill-pos-[REDACTED].db`. The owner then
  couldn't tell which snapshot lost its photos, and
  `TestRunDailyBackup_NoPhotosRaisesThenResolvesProblem` failed about 10%
  of the time.
- `internal/logging/redact.go`: there is a new `isDateTimeStamp` check, and
  `looksLikePAN` returns false when it matches. It matches only an exact
  15-char `20YYMMDD-HHMMSS` span that `time.Parse` accepts as a real date
  and time. No card scheme groups its digits 8-6, so the exemption costs
  no card coverage. The same digits without the dash are still checked.
- ut-cloud `internal/logredact` mirrors the change byte for byte
  (`TestRedactMirrorsPOS`).

## Tests (`internal/logging/redact_test.go`)

- Keep-lines: two Luhn-valid stamps in a no-photos message. These failed
  before the fix with `unitill-pos-[REDACTED].db`; the reviewer re-verified
  this in a worktree for both repos.
- Still-redacted samples:
  - Luhn-valid 8-6 runs with an invalid month or hour.
  - The undashed form.
  - A valid date and time with a non-20xx year (`36011231-235008`). This
    one was confirmed to fail without the year pin.
  - A card next to a stamp.

## Findings

1. minor (perf) — fixed. On a miss, `time.Parse` builds a `*ParseError`
   (4 allocs) for every 15-char span, such as a contiguous Amex number. The
   `20` year prefix check now runs first, so only 20xx stamp-shaped spans
   reach `time.Parse`.
2. nit (scope) — fixed. The year accepted 0000–9999. It is now pinned to
   20xx, which is all the backup emits.
3. nit (duplicated layout) — accepted. A comment cross-references
   `internal/db` `backupTimeLayout`. `internal/db` imports `logging`, so
   sharing the const would create an import cycle. If the backup name
   format ever changes, the server test shows it.

## Verified beyond unit tests

- The reviewer ran an adversarial probe: 336k random Luhn-valid PANs
  (13–19 digits, scheme groupings, space, dash or no separator) in 14
  contexts, including next to a stamp. There were zero leaks. 37k
  consecutive stamps over 3 days all survived.
- `TestRunDailyBackup_*`: 30, 40 and 10 consecutive runs, all passed.
- `time.Parse` leniency: single-digit fields, `24:00`, seconds `60`,
  29 Feb in a non-leap year and space separators are all not exempt.

## Gate

- gofmt, `go build ./...`, `go vet`, and `go test ./...` in both repos.
- The ci.yml guards pass, apart from two checks this container can't run.
  `guard-shellcheck-version` fails because shellcheck isn't installed here.
  golangci-lint fails because the container's binary was built with go1.25
  (ut-docs#4001). CI runs both.

Verdict: safe to merge. Merge universal-till first, then ut-cloud, because
the ut-cloud mirror guard checks against POS `main`.
