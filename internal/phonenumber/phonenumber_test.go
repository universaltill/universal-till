package phonenumber

import (
	"regexp"
	"testing"
)

func TestNormalise(t *testing.T) {
	for _, tc := range []struct {
		name, raw, region, want string
		ok                      bool
	}{
		// GB: trunk "0".
		{"GB national", "020 7946 0018", "GB", "+442079460018", true},
		{"GB national mobile dashes", "07700-900123", "GB", "+447700900123", true},
		{"GB 00-prefixed", "0044 20 7946 0018", "GB", "+442079460018", true},
		{"GB +-prefixed with (0)", "+44 (20) 7946 0018", "GB", "+442079460018", true},
		{"GB +44 (0) trunk in brackets", "+44 (0)20 7946 0018", "GB", "+442079460018", true},
		{"DE 0049 (0) trunk in brackets", "0049 (0)30 1234567", "DE", "+49301234567", true},
		{"GB lowercase region", "020 7946 0018", "gb", "+442079460018", true},
		// DE: trunk "0".
		{"DE national", "030 1234567", "DE", "+49301234567", true},
		{"DE formatted", "(0)30/1234567", "DE", "", false}, // '/' is not an accepted character
		{"DE parens", "(030) 123-4567", "DE", "+49301234567", true},
		{"DE international", "+49 30 1234567", "DE", "+49301234567", true},
		{"DE 00", "0049301234567", "DE", "+49301234567", true},
		// US: NANP trunk "1".
		{"US ten digits", "(212) 555-0123", "US", "+12125550123", true},
		{"US with trunk 1", "1-212-555-0123", "US", "+12125550123", true},
		{"US dotted", "212.555.0123", "US", "+12125550123", true},
		{"US international", "+1 212 555 0123", "US", "+12125550123", true},
		// IT: no trunk prefix — the leading 0 stays in the number.
		{"IT landline keeps 0", "06 1234 5678", "IT", "+390612345678", true},
		{"IT mobile", "312 345 6789", "IT", "+393123456789", true},
		{"IT international", "+39 06 1234 5678", "IT", "+390612345678", true},
		// RU: trunk "8".
		{"RU national 8", "8 (495) 123-45-67", "RU", "+74951234567", true},
		{"RU international", "+7 495 123 45 67", "RU", "+74951234567", true},
		{"RU 00", "007 495 1234567", "RU", "+74951234567", true},
		// TR: trunk "0".
		{"TR national", "0212 345 67 89", "TR", "+902123456789", true},
		{"TR mobile", "0532 123 45 67", "TR", "+905321234567", true},
		{"TR international", "+90 532 123 45 67", "TR", "+905321234567", true},
		// International numbers ignore the region, even an unknown one.
		{"international, unknown region", "+44 20 7946 0018", "ZZ", "+442079460018", true},
		{"international, empty region", "0044 20 7946 0018", "", "+442079460018", true},
		// Failures.
		{"national, unknown region", "020 7946 0018", "ZZ", "", false},
		{"national, empty region", "020 7946 0018", "", "", false},
		{"letters", "020 7946 ABCD", "GB", "", false},
		{"vanity letters", "1-800-FLOWERS", "US", "", false},
		{"plus not leading", "44+2079460018", "GB", "", false},
		{"two pluses", "++442079460018", "GB", "", false},
		{"empty", "", "GB", "", false},
		{"only formatting", " ( ) - ", "GB", "", false},
		{"too short", "+44 123", "GB", "", false},
		{"too short national", "0123", "GB", "", false},
		{"too long", "+44 1234 5678 9012 34", "GB", "", false},
		{"seven digits after plus", "+1234567", "", "", false},
		{"eight digits after plus", "+12345678", "", "+12345678", true},
		{"fifteen digits after plus", "+123456789012345", "", "+123456789012345", true},
		{"sixteen digits after plus", "+1234567890123456", "", "", false},
		{"star and hash dropped", "020 7946 0018#", "GB", "+442079460018", true},
		{"unicode digits refused", "０２０ 7946 0018", "GB", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Normalise(tc.raw, tc.region)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("Normalise(%q, %q) = (%q, %v), want (%q, %v)", tc.raw, tc.region, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestDigits(t *testing.T) {
	for raw, want := range map[string]string{
		"+44 (20) 7946-0018": "442079460018",
		"abc":                "",
		"":                   "",
		"*31#5":              "315",
	} {
		if got := Digits(raw); got != want {
			t.Errorf("Digits(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestTrailingKey(t *testing.T) {
	for raw, want := range map[string]string{
		"+44 20 7946 0018": "079460018", // last 9
		"079460018":        "079460018", // exactly 9
		"5550100":          "5550100",   // shorter: all digits
		"555010":           "555010",    // 6 digits: still a key
		"55501":            "",          // fewer than 6: no fallback
		"":                 "",
		"withheld":         "",
	} {
		if got := TrailingKey(raw); got != want {
			t.Errorf("TrailingKey(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestTable_Shape(t *testing.T) {
	isoRe := regexp.MustCompile(`^[A-Z]{2}$`)
	ccRe := regexp.MustCompile(`^[1-9][0-9]{0,2}$`)
	trunkRe := regexp.MustCompile(`^[0-9]{0,2}$`)
	if len(table) < 240 {
		t.Fatalf("table has %d entries, want every ITU-assigned region (~245)", len(table))
	}
	seen := map[string]bool{}
	for _, e := range table {
		if !isoRe.MatchString(e.ISO) {
			t.Errorf("ISO %q is not two uppercase letters", e.ISO)
		}
		if seen[e.ISO] {
			t.Errorf("ISO %q appears twice", e.ISO)
		}
		seen[e.ISO] = true
		if !ccRe.MatchString(e.CallingCode) {
			t.Errorf("%s: calling code %q is not 1-3 digits", e.ISO, e.CallingCode)
		}
		if !trunkRe.MatchString(e.Trunk) {
			t.Errorf("%s: trunk %q is not 0-2 digits", e.ISO, e.Trunk)
		}
	}
	if len(byISO) != len(table) {
		t.Errorf("lookup map has %d entries, table %d", len(byISO), len(table))
	}
}

func TestTable_SpotChecks(t *testing.T) {
	for _, tc := range []struct{ iso, cc, trunk string }{
		{"GB", "44", "0"},
		{"US", "1", "1"},
		{"CA", "1", "1"},
		{"IT", "39", ""},
		{"RU", "7", "8"},
		{"KZ", "7", "8"},
		{"BY", "375", "8"},
		{"TR", "90", "0"},
		{"DE", "49", "0"},
		{"FR", "33", "0"},
		{"ES", "34", ""},
		{"PT", "351", ""},
		{"HU", "36", "06"},
		{"MX", "52", ""},
		{"AE", "971", "0"},
		{"IR", "98", "0"},
	} {
		got, ok := Lookup(tc.iso)
		if !ok {
			t.Errorf("%s missing from the table", tc.iso)
			continue
		}
		if got.CallingCode != tc.cc || got.Trunk != tc.trunk {
			t.Errorf("%s = (%q, %q), want (%q, %q)", tc.iso, got.CallingCode, got.Trunk, tc.cc, tc.trunk)
		}
	}
	if _, ok := Lookup("ZZ"); ok {
		t.Error("ZZ must not resolve")
	}
	if _, ok := Lookup("gb"); !ok {
		t.Error("lookup must be case-insensitive")
	}
}
