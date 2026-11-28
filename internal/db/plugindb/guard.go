package plugindb

import (
	"errors"
	"strings"
)

// ErrStatementRefused is returned for SQL a plugin may not run through
// Exec/Query (ADR-0121 §4). The SQLITE_LIMIT_ATTACHED=0 connection limit is
// the real ATTACH guard; this check gives a clear error and closes the
// remaining holes (VACUUM INTO writes a file, PRAGMA changes the hardening,
// multi-statement strings, tampering with the migration ledger).
var ErrStatementRefused = errors.New("plugindb: statement refused")

// refusedFirst are statements a plugin never runs itself. ATTACH/DETACH/
// VACUUM/PRAGMA are ADR-0121 §4. Transaction control is refused as well: the
// host function is one statement per call on one pinned connection, so a
// plugin's BEGIN would leave a transaction open and every later "successful"
// write would be rolled back when the Store closes.
var refusedFirst = map[string]bool{
	"ATTACH": true, "DETACH": true, "VACUUM": true, "PRAGMA": true,
	"BEGIN": true, "COMMIT": true, "END": true, "ROLLBACK": true, "SAVEPOINT": true, "RELEASE": true,
}

// refusedAnywhere may not appear as a bare word anywhere in a migration, so
// the refusal does not rest on the statement splitter agreeing with SQLite.
// A plugin that really has a column with such a name quotes it.
var refusedAnywhere = map[string]bool{"ATTACH": true, "DETACH": true, "VACUUM": true, "PRAGMA": true}

// checkStatement refuses anything but a single, non-privileged statement.
func checkStatement(q string) error {
	kw, rest, ok := firstKeyword(q)
	if !ok {
		return ErrStatementRefused
	}
	if refusedFirst[strings.ToUpper(kw)] {
		return ErrStatementRefused
	}
	if hasSecondStatement(rest) {
		return ErrStatementRefused
	}
	if strings.Contains(strings.ToLower(q), "_ut_migrations") {
		return ErrStatementRefused
	}
	return nil
}

// splitMigration cuts a migration file into single statements and applies
// the same refusals as checkStatement to each one. A migration is plugin-
// authored text too: without this a PRAGMA in it would lift the storage quota
// or switch the hardening off for the connection's lifetime, and a COMMIT
// would break the one-transaction guarantee. A ';' inside a CREATE TRIGGER
// body does not end the statement; the body ends at the END that is not
// closing a CASE.
func splitMigration(q string) ([]string, error) {
	if strings.Contains(strings.ToLower(q), "_ut_migrations") {
		return nil, ErrStatementRefused
	}
	var out []string
	start, i := 0, 0
	var words []string // leading bare words of the current statement
	hasTok, trigger, caseDepth := false, false, 0
	flush := func(end int) error {
		if hasTok {
			if len(words) == 0 || refusedFirst[words[0]] {
				return ErrStatementRefused
			}
			out = append(out, q[start:end])
		}
		start, words, hasTok, trigger, caseDepth = end+1, nil, false, false, 0
		return nil
	}
	for i < len(q) {
		c := q[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			hasTok = true
			j := i + 1
			for j < len(q) {
				if q[j] == c {
					if j+1 < len(q) && q[j+1] == c {
						j += 2
						continue
					}
					break
				}
				j++
			}
			i = j + 1
		case c == '[':
			hasTok = true
			j := strings.IndexByte(q[i:], ']')
			if j < 0 {
				return nil, ErrStatementRefused
			}
			i += j + 1
		case strings.HasPrefix(q[i:], "--"):
			j := strings.IndexByte(q[i:], '\n')
			if j < 0 {
				i = len(q)
			} else {
				i += j + 1
			}
		case strings.HasPrefix(q[i:], "/*"):
			j := strings.Index(q[i+2:], "*/")
			if j < 0 {
				i = len(q)
			} else {
				i += 2 + j + 2
			}
		case c == ';':
			if trigger {
				i++
				continue
			}
			if err := flush(i); err != nil {
				return nil, err
			}
			i++
		case isWordStart(c):
			j := i + 1
			for j < len(q) && (isWordStart(q[j]) || q[j] >= '0' && q[j] <= '9' || q[j] == '$') {
				j++
			}
			w := strings.ToUpper(q[i:j])
			if refusedAnywhere[w] {
				return nil, ErrStatementRefused
			}
			if !hasTok && len(words) == 0 || len(words) > 0 && len(words) < 3 {
				words = append(words, w)
				n := len(words)
				if words[0] == "CREATE" && w == "TRIGGER" && (n == 2 || n == 3 && (words[1] == "TEMP" || words[1] == "TEMPORARY")) {
					trigger = true
				}
			}
			switch {
			case w == "CASE":
				caseDepth++
			case w == "END" && caseDepth > 0:
				caseDepth--
			case w == "END" && trigger && len(words) > 1:
				trigger = false // the next ';' ends the CREATE TRIGGER
			}
			hasTok = true
			i = j
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			i++
		default:
			hasTok = true
			i++
		}
	}
	if err := flush(len(q)); err != nil {
		return nil, err
	}
	return out, nil
}

func isWordStart(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c == '_' || c >= 0x80
}

// firstKeyword skips whitespace, -- comments and /* */ comments.
func firstKeyword(q string) (kw, rest string, ok bool) {
	i := 0
	for i < len(q) {
		switch {
		case q[i] == ' ' || q[i] == '\t' || q[i] == '\n' || q[i] == '\r' || q[i] == '\f' || q[i] == '\v':
			i++
		case strings.HasPrefix(q[i:], "--"):
			j := strings.IndexByte(q[i:], '\n')
			if j < 0 {
				return "", "", false
			}
			i += j + 1
		case strings.HasPrefix(q[i:], "/*"):
			j := strings.Index(q[i+2:], "*/")
			if j < 0 {
				return "", "", false
			}
			i += 2 + j + 2
		default:
			j := i
			for j < len(q) && (q[j] >= 'A' && q[j] <= 'Z' || q[j] >= 'a' && q[j] <= 'z') {
				j++
			}
			if j == i {
				return "", "", false
			}
			return q[i:j], q[j:], true
		}
	}
	return "", "", false
}

// hasSecondStatement reports a ';' outside quotes/comments that is followed
// by anything other than whitespace/comments.
func hasSecondStatement(s string) bool {
	i := 0
	semi := false
	for i < len(s) {
		c := s[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			if semi {
				return true
			}
			j := i + 1
			for j < len(s) {
				if s[j] == c {
					if j+1 < len(s) && s[j+1] == c {
						j += 2
						continue
					}
					break
				}
				j++
			}
			i = j + 1
		case c == '[':
			if semi {
				return true
			}
			j := strings.IndexByte(s[i:], ']')
			if j < 0 {
				return false
			}
			i += j + 1
		case strings.HasPrefix(s[i:], "--"):
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				return false
			}
			i += j + 1
		case strings.HasPrefix(s[i:], "/*"):
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				return semi
			}
			i += 2 + j + 2
		case c == ';':
			semi = true
			i++
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		default:
			if semi {
				return true
			}
			i++
		}
	}
	return false
}
