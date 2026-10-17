package catalogrules

import "testing"

func TestValidNetQuantity(t *testing.T) {
	v, zero, neg := int64(250), int64(0), int64(-1)
	g, kg := NetQuantityGrams, "kg"
	cases := []struct {
		name  string
		value *int64
		unit  *string
		want  bool
	}{
		{"none", nil, nil, true},
		{"grams", &v, &g, true},
		{"value without unit", &v, nil, false},
		{"unit without value", nil, &g, false},
		{"zero", &zero, &g, false},
		{"negative", &neg, &g, false},
		{"unknown unit", &v, &kg, false},
	}
	for _, c := range cases {
		if got := ValidNetQuantity(c.value, c.unit); got != c.want {
			t.Errorf("%s: ValidNetQuantity = %v, want %v", c.name, got, c.want)
		}
	}
}
