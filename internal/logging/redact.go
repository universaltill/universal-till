package logging

import (
	"regexp"
	"strings"
)

// redactedMark replaces every secret Redact removes.
const redactedMark = "[REDACTED]"

// Free-text log redaction for every log sink (ut-docs#2720): the log file,
// stdout/stderr and the Problems ring that bug-report bundles and the cloud
// heartbeat carry off the till (ut-docs#3145). The diagnostics
// stream (internal/diagnostics, ADR-0092) never carries free text at all —
// its events are closed enums plus ids validated against an id charset —
// so there is no string redactor there to reuse; this applies the same
// rule ("no credential, cookie or token ever leaves as data") to prose log
// lines, which can't be schema-checked. It errs on the side of removing
// too much: a false positive costs a log detail, a false negative leaks a
// credential onto disk or off the till.
var (
	// "Bearer <tok>" / "Basic <b64>" wherever they appear.
	authSchemeRe = regexp.MustCompile(`(?i)\b(bearer|basic)(\s+)[A-Za-z0-9._~+/=-]+`)

	// key=value / key: value / "key":"value" where the key names a secret.
	// Each alternative below is boundary-tolerant in its own way (ut-docs#2728):
	//   - "pin": bare \bpin, OR any prefix ending in "_"/"-" then "pin"
	//     ("admin_pin", "manager-pin"), plus an optional "_"/"-" suffix
	//     ("pin_code"). This is what makes "admin_pin"/"user_pin" match
	//     while "shipping"/"pinned"/"spin"/"pinning" (pin glued directly to
	//     other letters, no separator) do not.
	//   - "otp"/"totp"/"hotp": \b-bounded literal words only, so "photpx"
	//     (hotp mid-word, no boundary) survives; optional "_"/"-" suffix
	//     for "otp_code".
	//   - "session": optional "ut_"/"ut-" prefix, optional "_id"/"id"
	//     suffix — enumerated rather than suffix-tolerant so "sessions"
	//     and "session_count" (diagnostic counts) are NOT secrets.
	//   - "[?&]code": an OAuth authorization code in a query string
	//     (?code=/&code=) — bare "code" elsewhere (status/exit/error code,
	//     country_code, currency_code) is not a secret.
	//   - the enumerated *_code keys: one-time/pairing/device/... codes.
	secretKVRe = regexp.MustCompile(`(?i)((?:"|')?(?:[A-Za-z0-9_.-]*(?:token|secret|password|passwd|api[_-]?key|authorization|cookie|credential|private[_-]?key)[A-Za-z0-9_.-]*|(?:\bpin|[A-Za-z0-9.-]*[_-]pin)(?:[_-][A-Za-z0-9_.-]*)?|\bpwd|\b(?:otp|totp|hotp)(?:[_-][A-Za-z0-9_.-]*)?|\b(?:ut[_-])?session(?:[_-]?id)?|[?&]code|\b(?:auth|authorization|pairing|redeem|device|user|verification|recovery|reset)_code\b)(?:"|')?\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s,;&"'}\]]+)`)

	// scheme://user:password@host
	urlUserinfoRe = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)[^/\s:@]+:[^/\s@]+@`)

	// JWTs.
	jwtRe = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}`)

	// Unlabelled high-entropy runs (hex/base64 tokens, 32+ chars). '/' and
	// '=' are deliberately NOT in the run so file paths and "KEY=value"
	// labels split into separate words and are judged on their own.
	longTokenRe = regexp.MustCompile(`[A-Za-z0-9_+-]{32,}`)
	uuidRe      = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

	// panRunRe finds maximal runs of digit groups joined by a single space
	// or dash (ut-docs#2728), of any length. A card number is contiguous
	// ("4111111111111111") or grouped ("4111 1111 1111 1111" /
	// "4111-1111-1111-1111" / Amex "3782 822463 10005"), but in a log line
	// it is often glued to a neighbouring number with the same separator
	// ("qty 2 4111111111111111", "table 12 4111 1111 1111 1111",
	// "4111111111111111 5"), so redactPANs checks every group-aligned
	// sub-span of the run, not just the run as a whole. RE2 has no
	// backreference to require the SAME separator throughout, so each span
	// is re-checked in Go by looksLikePAN — which is also where the Luhn
	// and IIN checks live, since neither is expressible as a regexp either.
	panRunRe = regexp.MustCompile(`\b\d+(?:[ -]\d+)*\b`)
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
		// The unquoted value stops before ']', so an already-redacted value
		// reads as "[REDACTED" — leave it be, so a second pass is a no-op
		// (the ring, bundles and uploads each redact; ut-docs#3145).
		if sub[2] == redactedMark || sub[2] == strings.TrimSuffix(redactedMark, "]") {
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
	line = redactPANs(line)
	return line
}

// redactPANs replaces card numbers (ut-docs#2728): within each panRunRe
// run, a span of whole digit groups holding 13-19 digits is redacted only
// when looksLikePAN confirms it — a Luhn-invalid run, a 13-digit unix-ms
// timestamp (leads with 1, not a card IIN), a short order number and a
// UUID all fail that check and survive untouched. Spans always start and
// end on a group boundary, so part of a longer contiguous digit run is
// never cut out. Scanning left to right, the longest confirmed span from
// the earliest group start wins and scanning resumes after it. Work is
// linear in the run length: a span holds at most 19 digits, hence at most
// 19 groups, so each start position tries at most 19 spans.
func redactPANs(line string) string {
	runs := panRunRe.FindAllStringIndex(line, -1)
	if runs == nil {
		return line
	}
	var b strings.Builder
	last := 0
	for _, run := range runs {
		for _, span := range panSpans(line, run[0], run[1]) {
			b.WriteString(line[last:span[0]])
			b.WriteString(redactedMark)
			last = span[1]
		}
	}
	if last == 0 {
		return line
	}
	b.WriteString(line[last:])
	return b.String()
}

// panSpans returns the [start,end) offsets, in order and non-overlapping,
// of the confirmed card numbers inside the digit-group run line[rs:re].
func panSpans(line string, rs, re int) [][2]int {
	// Split the run into its digit groups.
	var groups [][2]int
	for i := rs; i < re; {
		j := i
		for j < re && line[j] >= '0' && line[j] <= '9' {
			j++
		}
		groups = append(groups, [2]int{i, j})
		i = j + 1 // skip the single separator
	}
	var out [][2]int
	for i := 0; i < len(groups); {
		best := -1
		digits := 0
		for j := i; j < len(groups); j++ {
			digits += groups[j][1] - groups[j][0]
			if digits > 19 {
				break
			}
			if digits >= 13 && looksLikePAN(line[groups[i][0]:groups[j][1]]) {
				best = j
			}
		}
		if best < 0 {
			i++
			continue
		}
		out = append(out, [2]int{groups[i][0], groups[best][1]})
		i = best + 1
	}
	return out
}

// looksLikePAN validates one group-aligned digit span from panSpans: a single consistent
// separator (space, dash, or none) throughout, a real card IIN (first
// digit 2-6 — a Unix-ms timestamp leads with 1), and a valid Luhn
// checksum.
func looksLikePAN(candidate string) bool {
	var sep byte
	haveSep := false
	digits := make([]byte, 0, len(candidate))
	for i := 0; i < len(candidate); i++ {
		c := candidate[i]
		if c == ' ' || c == '-' {
			if !haveSep {
				sep, haveSep = c, true
			} else if c != sep {
				return false
			}
			continue
		}
		digits = append(digits, c)
	}
	if len(digits) < 13 || len(digits) > 19 {
		return false
	}
	if digits[0] < '2' || digits[0] > '6' {
		return false
	}
	return luhnValid(digits)
}

// luhnValid implements the standard Luhn checksum (mod 10, doubling every
// second digit counted from the rightmost).
func luhnValid(digits []byte) bool {
	sum := 0
	double := false
	for i := len(digits) - 1; i >= 0; i-- {
		d := int(digits[i] - '0')
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return sum%10 == 0
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
