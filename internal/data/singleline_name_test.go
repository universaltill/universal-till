package data

import (
	"errors"
	"testing"

	"github.com/universaltill/universal-till/catimport/catalogrules"
	"github.com/universaltill/universal-till/catimport/catalogrules/namecases"
)

func TestCheckSingleLineName(t *testing.T) {
	refused := map[string]string{
		"nul":          "a\x00b",
		"tab":          "a\tb",
		"lf":           "a\nb",
		"cr":           "a\rb",
		"esc":          "a\x1bb",
		"del":          "a\x7fb",
		"c1 nel":       "a\u0085b",
		"c1 csi":       "a\u009bb",
		"line sep":     "a\u2028b",
		"para sep":     "a\u2029b",
		"lre":          "a\u202ab",
		"rle":          "a\u202bb",
		"pdf":          "a\u202cb",
		"lro":          "a\u202db",
		"rlo":          "a\u202eb",
		"zwsp":         "a\u200bb",
		"bom":          "a\ufeffb",
		"soft hyphen":  "a\u00adb",
		"tag char":     "a\U000E0067b",
		"england flag": "a🏴\U000E0067\U000E0062\U000E0065\U000E006E\U000E0067\U000E007Fb",
		"invalid utf8": "a\xffb",
		"open isolate": "a\u2067b",
		"lone pdi":     "a\u2069b",
		"pdi first":    "a\u2069\u2067b",
		"two open":     "\u2066\u2067x\u2069",
	}
	for name, in := range refused {
		t.Run("refuse "+name, func(t *testing.T) {
			if _, err := CheckSingleLineName(in); !errors.Is(err, ErrNameInvalidCharacters) {
				t.Fatalf("CheckSingleLineName(%q) err = %v, want ErrNameInvalidCharacters", in, err)
			}
		})
	}
	accepted := map[string]string{
		"plain":           "Cola",
		"cay":             "Çay",
		"hebrew rlm":      "שלום\u200f 1",
		"arabic lrm alm":  "مرحبا\u200e\u061c 2",
		"isolates":        "\u2067שלום\u2069 1L",
		"nested isolates": "\u2066a\u2067b\u2069c\u2069",
		"persian zwnj":    "می\u200cخواهم",
		"chef zwj":        "👩\u200d🍳",
		"flag":            "🇩🇪",
		"heart fe0f":      "❤\ufe0f",
	}
	for name, in := range accepted {
		t.Run("accept "+name, func(t *testing.T) {
			got, err := CheckSingleLineName(in)
			if err != nil || got != in {
				t.Fatalf("CheckSingleLineName(%q) = %q, %v; want unchanged, nil", in, got, err)
			}
		})
	}
	if got, err := CheckSingleLineName(" Cola\n"); err != nil || got != "Cola" {
		t.Fatalf("trim: got %q, %v", got, err)
	}
	if got, err := CheckSingleLineName("  "); err != nil || got != "" {
		t.Fatalf("blank: got %q, %v", got, err)
	}
}

func TestCheckSingleLineName_DelegatesToCatalogrules(t *testing.T) {
	if ErrNameInvalidCharacters != catalogrules.ErrNameInvalidCharacters {
		t.Fatal("data.ErrNameInvalidCharacters must be the catalogrules value")
	}
	for _, c := range namecases.Cases() {
		got, err := CheckSingleLineName(c.In)
		want, wantErr := catalogrules.CheckSingleLineName(c.In)
		if got != want || err != wantErr {
			t.Errorf("%s: data = %q, %v; catalogrules = %q, %v", c.Label, got, err, want, wantErr)
		}
	}
}
