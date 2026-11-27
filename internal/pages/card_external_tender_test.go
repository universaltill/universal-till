package pages

import "testing"

// ut-docs#6755
func TestParseExternalCardFields(t *testing.T) {
	cases := []struct{ auth, last4, wantAuth, wantMask, wantErr string }{
		{"", "", "", "", ""},
		{" AB12CD ", " 4242 ", "AB12CD", "•••• 4242", ""},
		{"", "4242", "", "•••• 4242", ""},
		{"", "424", "", "", externalCardErrLast4},
		{"", "42424", "", "", externalCardErrLast4},
		{"", "4a42", "", "", externalCardErrLast4},
		{"", "4242424242424242", "", "", externalCardErrLast4},
		{"", "٤٢٤٢", "", "", externalCardErrLast4},
		{"ABCDEFGHIJKLM", "", "", "", externalCardErrAuthCode},
		{"AB-12", "", "", "", externalCardErrAuthCode},
		{"AB 12", "", "", "", externalCardErrAuthCode},
		{"ÄB12", "", "", "", externalCardErrAuthCode},
	}
	for _, c := range cases {
		a, m, e := parseExternalCardFields(c.auth, c.last4)
		if a != c.wantAuth || m != c.wantMask || e != c.wantErr {
			t.Errorf("(%q,%q) = %q,%q,%q want %q,%q,%q", c.auth, c.last4, a, m, e, c.wantAuth, c.wantMask, c.wantErr)
		}
	}
}
