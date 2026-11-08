package catalogrules

import (
	"errors"
	"testing"

	"github.com/universaltill/universal-till/catimport/catalogrules/namecases"
)

func TestCheckSingleLineName_Cases(t *testing.T) {
	for _, c := range namecases.Cases() {
		t.Run(c.Label, func(t *testing.T) {
			got, err := CheckSingleLineName(c.In)
			if c.OK {
				if err != nil || got != c.Want {
					t.Fatalf("CheckSingleLineName(%q) = %q, %v; want %q, nil", c.In, got, err, c.Want)
				}
				return
			}
			if !errors.Is(err, ErrNameInvalidCharacters) || got != "" {
				t.Fatalf("CheckSingleLineName(%q) = %q, %v; want refusal", c.In, got, err)
			}
		})
	}
}

func TestCheckSingleLineName_Blank(t *testing.T) {
	if got, err := CheckSingleLineName("  "); got != "" || err != nil {
		t.Fatalf("blank = %q, %v; want \"\", nil", got, err)
	}
}

func TestNamecasesFresh(t *testing.T) {
	a := namecases.Cases()
	a[0].In = "mutated"
	if namecases.Cases()[0].In == "mutated" {
		t.Fatal("Cases() must return a fresh slice")
	}
}
