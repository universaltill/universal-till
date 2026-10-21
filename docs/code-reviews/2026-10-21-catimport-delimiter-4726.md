# Code review: catimport separator detection (ut-docs#4726, part of #3377)

**Change:** `catimport.HeaderRow` (the header-row claim a file-rows import
entry is matched against) and `catimport.Parse` read the first record's
separator, comma, semicolon or tab, whichever is most frequent outside
quotes (tie or none: comma). `Parse` also drops a UTF-8 BOM before reading,
so a BOM in front of a quoted header no longer fails. Help topic
`catalog.md` (en, de, tr) says a German Excel CSV needs no conversion.

**Why:** the real Epos Now product export is `;`-separated. Read with the
default comma, its header was one field, so no plugin could claim the file
and the standard importer failed with "no name column". The same held for
any German Excel CSV.

**Tests (TDD, red first):** `;` and tab headers claim (BOM, quoted, CRLF);
a separator inside a quoted header field is not the separator; `Parse` reads
`;` and tab files; a BOM before a quoted header parses. The first three
failed with the one-column header, the BOM test with
`bare " in non-quoted-field`, before the fix.

**Review:** independent Opus 5.5 subagent (code written by Sonnet 5.5), read
only. Verdict on this diff: the sniff is correct for comma files, BOM,
quotes, empty input, a first line over 64 KiB, one column and uneven rows;
`Parse` neither drops nor duplicates bytes. One finding on this diff,
fixed: `Parse` kept a BOM in front of a quoted header (pre-existing; more
reachable now that `;` Excel files arrive here). An error from `Peek` is
deliberately ignored; the next `Read` retries the reader.

**Verified:** `go test ./...` in `catimport`; `go build ./...`, and
`go test` of `internal/catsync`, `internal/plugins` and `internal/pages`;
`guard-help-topics.sh`, `guard-help-drift.sh`. The Epos Now plugin's
invented fixture is claimed by its manifest headers through the real
`HeaderRow`/`HeadersClaim`.

**Not verified:** ut-cloud's import job (it picks this up on its next
`catimport` bump); help text in ar/fa (not edited, same structure).

**Verdict:** safe to merge. Release note: the Epos Now plugin's
`min_pos_version` is the first till release that carries this change.
