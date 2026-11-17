package pos

import (
	"strings"
	"testing"
)

// ut-docs#1043: ReadCountProtocol is the tolerant read side of the stored
// shifts.count_protocol blob.

func TestReadCountProtocol(t *testing.T) {
	type line struct{ denom, pieces, total int64 }
	cases := []struct {
		name       string
		raw        string
		lines      []line
		total      int64
		recorded   bool
		partial    bool
		unreadable bool
	}{
		{"AC1 three lines high to low", `{"10":1,"5000":2,"100":3}`, []line{{5000, 2, 10000}, {100, 3, 300}, {10, 1, 10}}, 10310, true, false, false},
		{"empty string", ``, nil, 0, false, false, false},
		{"whitespace", "  \n", nil, 0, false, false, false},
		{"empty object", `{}`, nil, 0, false, false, false},
		{"zero pieces only", `{"100":0}`, nil, 0, false, false, false},
		{"zero skipped, rest kept", `{"100":0,"200":1}`, []line{{200, 1, 200}}, 200, true, false, false},
		{"bad key dropped", `{"abc":3,"200":1}`, []line{{200, 1, 200}}, 200, true, true, false},
		{"truncated json", `{"100":`, nil, 0, true, false, true},
		{"array", `[1,2]`, nil, 0, true, false, true},
		{"bare string", `"x"`, nil, 0, true, false, true},
		{"nested object value", `{"a":{}}`, nil, 0, true, false, true},
		{"script key", `{"<script>":1}`, nil, 0, true, false, true},
		{"plus key", `{"+5":1}`, nil, 0, true, false, true},
		{"leading zero key", `{"05":1}`, nil, 0, true, false, true},
		{"negative key", `{"-1":1}`, nil, 0, true, false, true},
		{"fraction key", `{"1.5":1}`, nil, 0, true, false, true},
		{"zero key", `{"0":1}`, nil, 0, true, false, true},
		{"negative value", `{"100":-1,"200":1}`, []line{{200, 1, 200}}, 200, true, true, false},
		{"fractional value", `{"100":1.5}`, nil, 0, true, false, true},
		{"quoted number reads like the write gate", `{"100":"2"}`, []line{{100, 2, 200}}, 200, true, false, false},
		{"non-numeric string value", `{"100":"two"}`, nil, 0, true, false, true},
		{"pieces overflow", `{"9223372036854775807":2}`, nil, 0, true, false, true},
		{"running total overflow", `{"9223372036854775807":1,"100":1}`, []line{{9223372036854775807, 1, 9223372036854775807}}, 9223372036854775807, true, true, false},
		{"trailing garbage", `{"100":1} {"200":1}`, nil, 0, true, false, true},
		{"oversized", `{"100":1` + strings.Repeat(" ", 5000) + `}`, nil, 0, true, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ReadCountProtocol(c.raw)
			if got.Recorded != c.recorded || got.Partial != c.partial || got.Unreadable != c.unreadable {
				t.Fatalf("flags = recorded %v partial %v unreadable %v, want %v %v %v", got.Recorded, got.Partial, got.Unreadable, c.recorded, c.partial, c.unreadable)
			}
			if len(got.Lines) != len(c.lines) {
				t.Fatalf("lines = %+v, want %d", got.Lines, len(c.lines))
			}
			for i, l := range c.lines {
				g := got.Lines[i]
				if g.Denomination.Minor() != l.denom || g.Pieces != l.pieces || g.Total.Minor() != l.total {
					t.Errorf("line %d = %+v, want %+v", i, g, l)
				}
			}
			if got.Total.Minor() != c.total {
				t.Errorf("total = %d, want %d", got.Total.Minor(), c.total)
			}
		})
	}
}
