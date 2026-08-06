package pages

import (
	"bytes"
	"encoding/csv"
	"testing"
)

func TestCSVSafe(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain text", "Sparkling Water 500ml", "Sparkling Water 500ml"},
		{"equals formula", `=cmd|'/c calc'!A1`, `'=cmd|'/c calc'!A1`},
		{"plus formula", "+1+1", "'+1+1"},
		{"minus formula", "-1+1", "'-1+1"},
		{"at formula", "@SUM(A1:A2)", "'@SUM(A1:A2)"},
		{"leading tab", "\tsneaky", "'\tsneaky"},
		{"leading CR", "\rsneaky", "'\rsneaky"},
		{"minus in the middle is untouched", "well-known", "well-known"},
		{"equals in the middle is untouched", "a=b", "a=b"},
		// "-" alone is this codebase's own "no entity ID" sentinel (a
		// dozen-odd InsertAudit call sites), not a formula — must survive
		// unchanged or most audit-export rows get a spurious "'-" (ut-docs#195 review).
		{"lone hyphen sentinel is untouched", "-", "-"},
		{"a longer dash-prefixed value is still defused", "--", "'--"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := csvSafe(c.in); got != c.want {
				t.Errorf("csvSafe(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// Round-trips a crafted formula-shaped value through the SAME encoding/csv
// writer the real exports use, then re-parses it, proving the sanitizer
// survives real CSV encoding (quoting/escaping) rather than just being a
// string-prefix check in isolation — a value with an embedded comma/quote
// exercises encoding/csv's own quoting alongside the leading-apostrophe
// mitigation.
func TestCSVSafeRoundTripsThroughRealCSVWriter(t *testing.T) {
	malicious := `=cmd|'/c calc'!A1,"with a comma and quote"`
	var buf bytes.Buffer
	cw := csv.NewWriter(&buf)
	if err := cw.Write([]string{"Actor", csvSafe(malicious)}); err != nil {
		t.Fatalf("write: %v", err)
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	cr := csv.NewReader(bytes.NewReader(buf.Bytes()))
	record, err := cr.Read()
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(record) != 2 {
		t.Fatalf("record = %v, want 2 fields", record)
	}
	got := record[1]
	if got[0] != '\'' {
		t.Fatalf("round-tripped field %q does not start with the defusing apostrophe", got)
	}
	if got != "'"+malicious {
		t.Fatalf("round-tripped field = %q, want %q", got, "'"+malicious)
	}
}
