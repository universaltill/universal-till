package httpx

import "strings"

// Staff languages (ut-docs#3086): the languages the ☰ Menu's language row
// offers. A shop sets them once in Settings; nothing else is filtered — the
// Settings language picker and the setup wizard still list every installed
// locale, and a ?lang= link to an unlisted locale still works for the
// browser that follows it.

// MatchLocale maps a locale tag onto an available locale code: the exact
// code if installed, else its base language ("de-DE" → "de"), else "".
func MatchLocale(tag string, available []string) string {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return ""
	}
	for _, a := range available {
		if strings.EqualFold(a, tag) {
			return a
		}
	}
	base := tag
	if i := strings.IndexAny(base, "-_"); i > 0 {
		base = base[:i]
	}
	for _, a := range available {
		if strings.EqualFold(a, base) {
			return a
		}
	}
	return ""
}

// ParseStaffLocales splits a stored staff-languages value (comma-separated
// locale codes) into its trimmed, non-empty entries.
func ParseStaffLocales(stored string) []string {
	var out []string
	for _, s := range strings.Split(stored, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// StaffLocalesFor is the effective staff-language list, in the order of
// available: the stored selection's installed entries, always plus the
// shop's default language. Unset (stored is blank) means the default
// language plus English — which is also what every shop set up before
// ut-docs#3086 gets, without any write to its settings.
func StaffLocalesFor(stored, defaultLocale string, available []string) []string {
	want := map[string]bool{}
	if def := MatchLocale(defaultLocale, available); def != "" {
		want[def] = true
	}
	sel := ParseStaffLocales(stored)
	if len(sel) == 0 {
		sel = []string{"en"}
	}
	for _, s := range sel {
		if m := MatchLocale(s, available); m != "" {
			want[m] = true
		}
	}
	out := make([]string, 0, len(want))
	for _, a := range available {
		if want[a] {
			out = append(out, a)
		}
	}
	return out
}

// StaffLocales is StaffLocalesFor with this till's default and installed
// locales.
func StaffLocales(stored string) []string {
	return StaffLocalesFor(stored, DefaultLocale(), AvailableLocales())
}

// DefaultStaffLocale is the installed locale code the shop's default
// language maps to — the one entry the staff-language list can't drop.
// "" when the default isn't installed.
func DefaultStaffLocale() string {
	return MatchLocale(DefaultLocale(), AvailableLocales())
}
