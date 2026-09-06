package data

import "testing"

// ut-docs#1610 scoping guard: stripRetireMangle undoes ONLY deleteMissing's
// exact "<value>~<own id>" suffix. A legitimately-named active row — even one
// that happens to contain a literal '~', or ends in someone ELSE's id — must
// come back byte-for-byte unchanged, or the strip would silently truncate real
// names the operator typed.
func TestStripRetireMangle_OnlyExactOwnIDSuffix(t *testing.T) {
	cases := []struct {
		id, name, want string
	}{
		// The one case that must change: the retire-in-place mangle.
		{"b1", "Acme Foods~b1", "Acme Foods"},
		{"u-alice", "alice~u-alice", "alice"},
		// Active, never-mangled rows: unchanged.
		{"b1", "Acme Foods", "Acme Foods"},
		{"b1", "Tilde~Brand", "Tilde~Brand"},       // literal '~' mid-name
		{"b1", "Acme Foods~", "Acme Foods~"},       // trailing '~' but no id
		{"b1", "Acme Foods~b2", "Acme Foods~b2"},   // some OTHER row's id
		{"b1", "Acme Foods~b10", "Acme Foods~b10"}, // longer id sharing a prefix
		{"b1", "Acme Foods~b1 ", "Acme Foods~b1 "}, // trailing space: not an exact suffix
		{"b1", "Acme Foods~B1", "Acme Foods~B1"},   // ids are case-sensitive
		{"b1", "Acme Foods ~b1", "Acme Foods "},    // space before '~' is part of the name; still the exact mangle
		{"", "Trailing~", "Trailing~"},             // empty id must not strip a bare '~'
		{"b1", "~b1", ""},                          // degenerate: a name that was only ever the mangle
		{"b1", "Acme~b1~b1", "Acme~b1"},            // strips exactly one suffix (the CASE keeps the mangle single)
	}
	for _, c := range cases {
		if got := stripRetireMangle(c.id, c.name); got != c.want {
			t.Errorf("stripRetireMangle(%q, %q) = %q, want %q", c.id, c.name, got, c.want)
		}
	}
}
