package httpx

import (
	"html/template"
	"net/http"
	"strings"
	"sync/atomic"
)

// Translate mode (ADR-0160 §7, ut-docs#4370): a manager turns it on to see
// which key each on-screen string comes from and open that string in
// Weblate. In this mode only, the T/TM template helpers tag every rendered
// string with its key.
//
// Why a marker and not the data-i18n-key attribute itself: T returns a
// string that templates drop into text, attribute values, <title> and
// inline JS strings alike. A `<span data-i18n-key>` wrapper would be
// escaped into visible markup in three of those four contexts. So T
// appends an invisible marker instead — the key spelled in Unicode tag
// characters (U+E0020..U+E007E, default-ignorable: browsers paint
// nothing), between U+E0001 and U+E007F — and web/public/translate-mode.js
// turns each marker into a data-i18n-key attribute on the element that
// shows the string, removing the marker. Outside translate mode T is
// byte-for-byte unchanged.

const (
	keyMarkStart = '\U000E0001' // LANGUAGE TAG
	keyMarkEnd   = '\U000E007F' // CANCEL TAG
	tagBase      = 0xE0000
)

// translateModeSource is the per-request "is translate mode on" check,
// wired by pages.Init (the cookie, the override editor's permission, and
// never on a self-order kiosk). nil (tests, a boot before Init) = off.
var translateModeSource atomic.Value // func(*http.Request) bool

// InitTranslateMode wires the translate-mode check; nil turns it off.
func InitTranslateMode(check func(*http.Request) bool) {
	translateModeSource.Store(check)
}

// TranslateModeOn reports whether r renders in translate mode.
func TranslateModeOn(r *http.Request) bool {
	check, _ := translateModeSource.Load().(func(*http.Request) bool)
	return check != nil && r != nil && check(r)
}

// MarkKey appends key's invisible marker to value. A key with a byte
// outside printable ASCII (a plugin may ship one) is left unmarked: the
// tag block only spells ASCII.
func MarkKey(key, value string) string {
	if key == "" {
		return value
	}
	var b strings.Builder
	b.Grow(len(value) + 4*(len(key)+2))
	b.WriteString(value)
	b.WriteRune(keyMarkStart)
	for i := 0; i < len(key); i++ {
		c := key[i]
		if c < 0x20 || c > 0x7E {
			return value
		}
		b.WriteRune(rune(tagBase + int(c)))
	}
	b.WriteRune(keyMarkEnd)
	return b.String()
}

// withTranslateMode binds the translate-mode template funcs for one
// request: "translatemode" (base.html loads the panel and its script only
// when true), marking T/TM, and a shell signature that differs from the
// normal one so a boosted navigation never swaps a translate-mode page into
// a normal shell or the other way round (ADR-0098). Off: only
// "translatemode" changes (to false), so output is unchanged.
func withTranslateMode(funcs template.FuncMap, r *http.Request) template.FuncMap {
	if !TranslateModeOn(r) {
		funcs["translatemode"] = func() bool { return false }
		return funcs
	}
	funcs["translatemode"] = func() bool { return true }
	if t, ok := funcs["T"].(func(string) string); ok {
		funcs["T"] = func(key string) string { return MarkKey(key, t(key)) }
	}
	if tm, ok := funcs["TM"].(func(string, ...any) string); ok {
		funcs["TM"] = func(key string, pairs ...any) string { return MarkKey(key, tm(key, pairs...)) }
	}
	if sig, ok := funcs["shellsig"].(func(any) string); ok {
		funcs["shellsig"] = func(theme any) string { return sig(theme) + "|translate-mode" }
	}
	return funcs
}
