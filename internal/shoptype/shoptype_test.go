package shoptype

import "testing"

func TestKnownTypes(t *testing.T) {
	for _, v := range List() {
		if !IsKnown(v) {
			t.Errorf("%q listed but not known", v)
		}
	}
	for _, v := range []string{"", "Cafe", "spaceship", " cafe"} {
		if IsKnown(v) {
			t.Errorf("%q must not be known", v)
		}
	}
	if len(List()) != 6 {
		t.Errorf("ADR-0026 has six types, got %v", List())
	}
	l := List()
	l[0] = "x"
	if List()[0] == "x" {
		t.Error("List must return a copy")
	}
}
