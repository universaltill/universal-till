// Package msgfmt is the till's ICU MessageFormat subset (ADR-0160 §8,
// ut-docs#4373): enough for translators to write correct plurals in every
// language without a placeholder per grammatical form.
//
// Supported:
//
//	{name}                          the argument's value (numbers formatted)
//	{name, number}                  the argument as a number
//	{name, number, integer}         the same, rounded to a whole number
//	{name, plural, [offset:N] =N {…} one {…} other {…}}
//	                                CLDR cardinal categories (zero, one, two,
//	                                few, many, other); `#` inside a branch is
//	                                the number minus the offset
//	{name, select, a {…} other {…}} a branch per string value
//
// Quoting follows ICU's default apostrophe mode: `”` is a literal
// apostrophe, and a single `'` starts quoted literal text only when the next
// character is `{`, `}`, `|` or (inside a plural branch) `#`; any other `'`
// is just an apostrophe, so "l'article" needs no escaping.
//
// Anything else (selectordinal, date/time, a missing `other` branch, an
// unknown argument, unbalanced braces) is an error. Callers fall back to
// English on an error rather than render broken text (config.I18n.TM).
package msgfmt

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"golang.org/x/text/feature/plural"
	"golang.org/x/text/language"
)

// Options tunes Format.
type Options struct {
	// Number renders a number for `#`, {n} and {n, number}. It receives the
	// plain decimal text (Go's "-1234.5" shape, no grouping) so it can group
	// and shape digits for the display locale. Nil renders the plain text.
	Number func(plain string) string
}

// Message is a parsed pattern, reusable across Format calls.
type Message struct {
	parts []part
}

// part is one piece of a message: literal text, `#`, or an argument.
type part struct {
	text  string // literal (kind == partText)
	kind  partKind
	arg   string // argument name
	style string // number style ("" or "integer")
	// plural/select
	offset   float64
	exact    []exactBranch
	branches map[string]*Message
}

type partKind int

const (
	partText partKind = iota
	partHash
	partSimple
	partNumber
	partPlural
	partSelect
)

type exactBranch struct {
	value float64
	msg   *Message
}

// pluralKeywords are the CLDR cardinal categories a plural branch may name.
var pluralKeywords = map[string]plural.Form{
	"zero":  plural.Zero,
	"one":   plural.One,
	"two":   plural.Two,
	"few":   plural.Few,
	"many":  plural.Many,
	"other": plural.Other,
}

var formNames = map[plural.Form]string{
	plural.Zero:  "zero",
	plural.One:   "one",
	plural.Two:   "two",
	plural.Few:   "few",
	plural.Many:  "many",
	plural.Other: "other",
}

// Parse parses pattern into a Message.
func Parse(pattern string) (*Message, error) {
	p := &parser{s: pattern}
	m, err := p.message(false)
	if err != nil {
		return nil, err
	}
	if p.pos < len(p.s) {
		return nil, p.errf("unexpected '}'")
	}
	return m, nil
}

// Format parses pattern and formats it for locale (a BCP 47 tag; plural
// rules come from its language) with args.
func Format(locale, pattern string, args map[string]any, opts Options) (string, error) {
	m, err := Parse(pattern)
	if err != nil {
		return "", err
	}
	return m.Format(locale, args, opts)
}

// Format renders m for locale with args.
func (m *Message) Format(locale string, args map[string]any, opts Options) (string, error) {
	tag, err := language.Parse(locale)
	if err != nil {
		tag = language.English
	}
	var b strings.Builder
	if err := m.format(&b, tag, args, opts, nil); err != nil {
		return "", err
	}
	return b.String(), nil
}

// hashValue is the number `#` renders inside the innermost plural branch.
type hashValue struct{ plain string }

func (m *Message) format(b *strings.Builder, tag language.Tag, args map[string]any, opts Options, hash *hashValue) error {
	for i := range m.parts {
		p := &m.parts[i]
		switch p.kind {
		case partText:
			b.WriteString(p.text)
		case partHash:
			if hash == nil {
				b.WriteByte('#')
				continue
			}
			b.WriteString(number(opts, hash.plain))
		case partSimple:
			v, ok := args[p.arg]
			if !ok {
				return fmt.Errorf("msgfmt: missing argument %q", p.arg)
			}
			if plain, ok := numericPlain(v); ok {
				b.WriteString(number(opts, plain))
			} else {
				b.WriteString(fmt.Sprint(v))
			}
		case partNumber:
			plain, err := numArg(args, p.arg)
			if err != nil {
				return err
			}
			if p.style == "integer" {
				f, _ := strconv.ParseFloat(plain, 64)
				plain = strconv.FormatFloat(math.Round(f), 'f', 0, 64)
			}
			b.WriteString(number(opts, plain))
		case partSelect:
			v, ok := args[p.arg]
			if !ok {
				return fmt.Errorf("msgfmt: missing argument %q", p.arg)
			}
			sub, ok := p.branches[fmt.Sprint(v)]
			if !ok {
				sub = p.branches["other"]
			}
			if err := sub.format(b, tag, args, opts, hash); err != nil {
				return err
			}
		case partPlural:
			plain, err := numArg(args, p.arg)
			if err != nil {
				return err
			}
			n, _ := strconv.ParseFloat(plain, 64)
			sub := p.selectPlural(tag, n, plain)
			h := &hashValue{plain: plain}
			if p.offset != 0 {
				h.plain = trimFloat(n - p.offset)
			}
			if err := sub.format(b, tag, args, opts, h); err != nil {
				return err
			}
		}
	}
	return nil
}

// selectPlural picks the branch: an exact `=N` match on the number itself
// first, then the CLDR category of the number minus the offset, then other.
func (p *part) selectPlural(tag language.Tag, n float64, plain string) *Message {
	for _, e := range p.exact {
		if e.value == n {
			return e.msg
		}
	}
	if p.offset != 0 {
		plain = trimFloat(n - p.offset)
	}
	if sub, ok := p.branches[formNames[category(tag, plain)]]; ok {
		return sub
	}
	return p.branches["other"]
}

// category returns the CLDR cardinal plural form of a plain decimal string
// for tag, from its operands (i, v, w, f, t — UTS #35).
func category(tag language.Tag, plain string) plural.Form {
	plain = strings.TrimPrefix(plain, "-")
	intPart, frac, _ := strings.Cut(plain, ".")
	trimmed := strings.TrimRight(frac, "0")
	return plural.Cardinal.MatchPlural(tag,
		operand(intPart), len(frac), len(trimmed), operand(frac), operand(trimmed))
}

// operand parses a digit string. A value too big for an int keeps its last
// 9 digits plus 10^9, so it stays large (never reads as 0 or 1, which
// en/fr rules test for) while every modulo a CLDR rule takes is unchanged.
func operand(digits string) int {
	if len(digits) <= 9 {
		n, _ := strconv.Atoi(digits) // "" (no fraction) is 0
		return n
	}
	if n, err := strconv.Atoi(digits); err == nil {
		return n
	}
	n, _ := strconv.Atoi(digits[len(digits)-9:])
	return n + 1_000_000_000
}

func trimFloat(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func number(opts Options, plain string) string {
	if opts.Number != nil {
		return opts.Number(plain)
	}
	return plain
}

func numArg(args map[string]any, name string) (string, error) {
	v, ok := args[name]
	if !ok {
		return "", fmt.Errorf("msgfmt: missing argument %q", name)
	}
	plain, ok := numericPlain(v)
	if !ok {
		return "", fmt.Errorf("msgfmt: argument %q is not a number: %v", name, v)
	}
	return plain, nil
}

// numericPlain renders a numeric value (any Go integer or float, or a
// string holding a plain decimal) as plain decimal text. Strings keep their
// own digits, so "1.50" keeps its visible trailing zero for the plural
// operands (CLDR: 1.50 is not "one" in English).
func numericPlain(v any) (string, bool) {
	switch n := v.(type) {
	case int:
		return strconv.FormatInt(int64(n), 10), true
	case int8:
		return strconv.FormatInt(int64(n), 10), true
	case int16:
		return strconv.FormatInt(int64(n), 10), true
	case int32:
		return strconv.FormatInt(int64(n), 10), true
	case int64:
		return strconv.FormatInt(n, 10), true
	case uint:
		return strconv.FormatUint(uint64(n), 10), true
	case uint8:
		return strconv.FormatUint(uint64(n), 10), true
	case uint16:
		return strconv.FormatUint(uint64(n), 10), true
	case uint32:
		return strconv.FormatUint(uint64(n), 10), true
	case uint64:
		return strconv.FormatUint(n, 10), true
	case float32:
		if math.IsNaN(float64(n)) || math.IsInf(float64(n), 0) {
			return "", false
		}
		return strconv.FormatFloat(float64(n), 'f', -1, 32), true
	case float64:
		return numericFloat(n)
	case string:
		if isPlainDecimal(n) {
			return n, true
		}
	}
	return "", false
}

func numericFloat(f float64) (string, bool) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "", false
	}
	return trimFloat(f), true
}

func isPlainDecimal(s string) bool {
	s = strings.TrimPrefix(s, "-")
	intPart, frac, hasDot := strings.Cut(s, ".")
	if intPart == "" || (hasDot && frac == "") {
		return false
	}
	for _, r := range intPart + frac {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ---- parser ----

type parser struct {
	s   string
	pos int
}

func (p *parser) errf(format string, a ...any) error {
	return fmt.Errorf("msgfmt: at offset %d: %s", p.pos, fmt.Sprintf(format, a...))
}

// message parses until the end of input or an unmatched '}' (left
// unconsumed). inPlural makes '#' special.
func (p *parser) message(inPlural bool) (*Message, error) {
	m := &Message{}
	var text strings.Builder
	flush := func() {
		if text.Len() > 0 {
			m.parts = append(m.parts, part{kind: partText, text: text.String()})
			text.Reset()
		}
	}
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		switch {
		case c == '\'':
			p.pos++
			if p.pos < len(p.s) && p.s[p.pos] == '\'' {
				text.WriteByte('\'')
				p.pos++
				continue
			}
			if p.pos < len(p.s) && quoteStarts(p.s[p.pos], inPlural) {
				if err := p.quoted(&text); err != nil {
					return nil, err
				}
				continue
			}
			text.WriteByte('\'')
		case c == '{':
			flush()
			a, err := p.argument()
			if err != nil {
				return nil, err
			}
			m.parts = append(m.parts, a)
		case c == '}':
			flush()
			return m, nil
		case c == '#' && inPlural:
			flush()
			m.parts = append(m.parts, part{kind: partHash})
			p.pos++
		default:
			text.WriteByte(c)
			p.pos++
		}
	}
	flush()
	return m, nil
}

func quoteStarts(c byte, inPlural bool) bool {
	return c == '{' || c == '}' || c == '|' || (c == '#' && inPlural)
}

// quoted copies literal text up to the closing apostrophe; `”` inside is
// one apostrophe. ICU lets an unclosed quote run to the end of the
// pattern; we treat it as an error so a stray quote can't swallow the rest
// of a translation.
func (p *parser) quoted(text *strings.Builder) error {
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		p.pos++
		if c != '\'' {
			text.WriteByte(c)
			continue
		}
		if p.pos < len(p.s) && p.s[p.pos] == '\'' {
			text.WriteByte('\'')
			p.pos++
			continue
		}
		return nil
	}
	return p.errf("unterminated quote")
}

func (p *parser) skipSpace() {
	for p.pos < len(p.s) && isSpace(p.s[p.pos]) {
		p.pos++
	}
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func isIdent(c byte) bool {
	return c == '_' || c == '-' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func (p *parser) ident() string {
	start := p.pos
	for p.pos < len(p.s) && isIdent(p.s[p.pos]) {
		p.pos++
	}
	return p.s[start:p.pos]
}

func (p *parser) expect(c byte) error {
	if p.pos >= len(p.s) {
		return p.errf("expected %q, got end of pattern", c)
	}
	if p.s[p.pos] != c {
		return p.errf("expected %q, got %q", c, p.s[p.pos])
	}
	p.pos++
	return nil
}

// argument parses `{name …}` starting at the '{'.
func (p *parser) argument() (part, error) {
	p.pos++ // '{'
	p.skipSpace()
	name := p.ident()
	if name == "" {
		return part{}, p.errf("expected an argument name")
	}
	p.skipSpace()
	if p.pos < len(p.s) && p.s[p.pos] == '}' {
		p.pos++
		return part{kind: partSimple, arg: name}, nil
	}
	if err := p.expect(','); err != nil {
		return part{}, err
	}
	p.skipSpace()
	typ := p.ident()
	p.skipSpace()
	switch typ {
	case "number":
		a := part{kind: partNumber, arg: name}
		if p.pos < len(p.s) && p.s[p.pos] == ',' {
			p.pos++
			p.skipSpace()
			a.style = p.ident()
			if a.style != "integer" {
				return part{}, p.errf("unsupported number style %q", a.style)
			}
			p.skipSpace()
		}
		return a, p.expect('}')
	case "plural", "select":
		if err := p.expect(','); err != nil {
			return part{}, err
		}
		return p.branches(name, typ == "plural")
	case "":
		return part{}, p.errf("expected an argument type")
	default:
		return part{}, p.errf("unsupported argument type %q", typ)
	}
}

// branches parses a plural/select body up to and including its closing '}'.
func (p *parser) branches(name string, isPlural bool) (part, error) {
	a := part{kind: partSelect, arg: name, branches: map[string]*Message{}}
	if isPlural {
		a.kind = partPlural
	}
	p.skipSpace()
	if isPlural && strings.HasPrefix(p.s[p.pos:], "offset:") {
		p.pos += len("offset:")
		p.skipSpace()
		start := p.pos
		for p.pos < len(p.s) && p.s[p.pos] >= '0' && p.s[p.pos] <= '9' {
			p.pos++
		}
		off, err := strconv.Atoi(p.s[start:p.pos])
		if err != nil {
			return part{}, p.errf("bad plural offset")
		}
		a.offset = float64(off)
	}
	seen := map[string]bool{}
	for {
		p.skipSpace()
		if p.pos >= len(p.s) {
			return part{}, p.errf("unterminated %s", map[bool]string{true: "plural", false: "select"}[isPlural])
		}
		if p.s[p.pos] == '}' {
			p.pos++
			break
		}
		var key string
		exact := isPlural && p.s[p.pos] == '='
		if exact {
			p.pos++
			start := p.pos
			for p.pos < len(p.s) && (p.s[p.pos] == '.' || (p.s[p.pos] >= '0' && p.s[p.pos] <= '9')) {
				p.pos++
			}
			key = "=" + p.s[start:p.pos]
		} else {
			key = p.ident()
		}
		if key == "" || key == "=" {
			return part{}, p.errf("expected a branch selector")
		}
		if isPlural && !exact {
			if _, ok := pluralKeywords[key]; !ok {
				return part{}, p.errf("unknown plural category %q", key)
			}
		}
		if seen[key] {
			return part{}, p.errf("duplicate branch %q", key)
		}
		seen[key] = true
		p.skipSpace()
		if err := p.expect('{'); err != nil {
			return part{}, err
		}
		sub, err := p.message(isPlural)
		if err != nil {
			return part{}, err
		}
		if err := p.expect('}'); err != nil {
			return part{}, err
		}
		if exact {
			v, err := strconv.ParseFloat(key[1:], 64)
			if err != nil {
				return part{}, p.errf("bad exact selector %q", key)
			}
			a.exact = append(a.exact, exactBranch{value: v, msg: sub})
		} else {
			a.branches[key] = sub
		}
	}
	if _, ok := a.branches["other"]; !ok {
		return part{}, fmt.Errorf("msgfmt: %q has no other branch", name)
	}
	return a, nil
}
