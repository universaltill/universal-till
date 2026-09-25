package logging

import (
	"regexp"
)

// redactedMark replaces every secret Redact removes.
const redactedMark = "[REDACTED]"

// Free-text log redaction for the log FILE (ut-docs#2720). The diagnostics
// stream (internal/diagnostics, ADR-0092) never carries free text at all —
// its events are closed enums plus ids validated against an id charset —
// so there is no string redactor there to reuse; this applies the same
// rule ("no credential, cookie or token ever leaves as data") to prose log
// lines, which can't be schema-checked. It errs on the side of removing
// too much: a false positive costs a log detail, a false negative leaks a
// credential onto disk.
var (
	// "Bearer <tok>" / "Basic <b64>" wherever they appear.
	authSchemeRe = regexp.MustCompile(`(?i)\b(bearer|basic)(\s+)[A-Za-z0-9._~+/=-]+`)

	// key=value / key: value / "key":"value" where the key names a secret.
	// "pin" only as a whole word (so "shipping"/"pinned" survive).
	secretKVRe = regexp.MustCompile(`(?i)((?:"|')?(?:[A-Za-z0-9_.-]*(?:token|secret|password|passwd|api[_-]?key|authorization|cookie|credential|private[_-]?key)[A-Za-z0-9_.-]*|\bpin|\bpwd)(?:"|')?\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s,;&"'}\]]+)`)

	// scheme://user:password@host
	urlUserinfoRe = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)[^/\s:@]+:[^/\s@]+@`)

	// JWTs.
	jwtRe = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}`)

	// Unlabelled high-entropy runs (hex/base64 tokens, 32+ chars). '/' and
	// '=' are deliberately NOT in the run so file paths and "KEY=value"
	// labels split into separate words and are judged on their own.
	longTokenRe = regexp.MustCompile(`[A-Za-z0-9_+-]{32,}`)
	uuidRe      = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
)

// Redact returns line with credentials, tokens and token-looking strings
// replaced by [REDACTED]. UUIDs (till/store/order ids), versions, paths,
// hosts and prose are kept — they are what makes a log diagnosable.
func Redact(line string) string {
	line = jwtRe.ReplaceAllString(line, redactedMark)
	line = authSchemeRe.ReplaceAllString(line, "${1}${2}"+redactedMark)
	line = urlUserinfoRe.ReplaceAllString(line, "${1}"+redactedMark+"@")
	line = secretKVRe.ReplaceAllStringFunc(line, func(m string) string {
		sub := secretKVRe.FindStringSubmatch(m)
		if sub[2] == redactedMark {
			return m
		}
		return sub[1] + redactedMark
	})
	line = longTokenRe.ReplaceAllStringFunc(line, func(m string) string {
		if !looksLikeToken(m) {
			return m
		}
		return redactedMark
	})
	return line
}

// looksLikeToken: a 32+ char run is a token when letters and digits
// alternate the way random hex/base64 does (≥ minClassSwitches switches
// between a letter and a digit) and it isn't just UUIDs with short labels
// ("till-<uuid>"). Readable identifiers — "ut-plugin-fiscal-de-tse2",
// "TestSomething123456" — cluster their digits and survive; a random
// 32-char hex or base64 string has ~10–15 switches.
func looksLikeToken(run string) bool {
	const minClassSwitches = 4
	if len(uuidRe.ReplaceAllString(run, "")) < 32 {
		return false
	}
	switches, prev := 0, byte(0)
	for i := 0; i < len(run); i++ {
		c := run[i]
		var class byte
		switch {
		case c >= '0' && c <= '9':
			class = 'd'
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
			class = 'a'
		default:
			continue
		}
		if prev != 0 && class != prev {
			switches++
		}
		prev = class
	}
	return switches >= minClassSwitches
}

// redactingWriter redacts each Write (one log line per Write from both
// log.Logger and this package) before passing it on.
type redactingWriter struct {
	w interface{ Write([]byte) (int, error) }
}

func (r redactingWriter) Write(p []byte) (int, error) {
	if _, err := r.w.Write([]byte(Redact(string(p)))); err != nil {
		return 0, err
	}
	return len(p), nil
}
