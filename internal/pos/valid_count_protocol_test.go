package pos

import (
	"strings"
	"testing"
)

// ut-docs#6159: the write gate must accept only what ReadCountProtocol can
// read back losslessly.
func TestValidCountProtocol_Table(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty object", `{}`, true},
		{"typical", `{"5000":2,"100":13}`, true},
		{"zero pieces", `{"100":0}`, true},
		{"non-numeric key", `{"abc":3}`, false},
		{"leading zero key", `{"0100":1}`, false},
		{"zero key", `{"0":1}`, false},
		{"negative key", `{"-5":1}`, false},
		{"whitespace key", `{" 100":1}`, false},
		{"quoted value", `{"100":"3"}`, false},
		{"fractional value", `{"100":3.5}`, false},
		{"exponent value", `{"100":1e2}`, false},
		{"negative value", `{"100":-1}`, false},
		{"null value", `{"100":null}`, false},
		{"bool value", `{"100":true}`, false},
		{"line overflow", `{"9223372036854775807":2}`, false},
		{"sum overflow", `{"4611686018427387904":1,"4611686018427387903":1,"2":1}`, false},
		{"array", `[1,2,3]`, false},
		{"string", `"hello"`, false},
		{"null", `null`, false},
		{"number", `5`, false},
		{"trailing data", `{"100":1} {}`, false},
		{"trailing close brace", `{"100":1} }`, false},
		{"trailing close bracket", `{"100":1}]`, false},
		{"trailing whitespace", "{\"100\":1}\n", true},
		{"duplicate key", `{"100":1,"100":3}`, false},
		{"duplicate key hiding a quoted value", `{"100":"x","100":1}`, false},
		{"nested", `{"100":{"a":1}}`, false},
		{"oversize", `{"100":1` + strings.Repeat(" ", maxCountProtocolLen) + `}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ValidCountProtocol(c.in); got != c.want {
				t.Fatalf("ValidCountProtocol(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
