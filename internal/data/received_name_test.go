package data

import (
	"errors"
	"testing"

	"github.com/universaltill/universal-till/catimport/catalogrules/namecases"
)

// ut-docs#5871: checkReceivedName is CheckSingleLineName plus the legacy
// tolerance for a name equal to the stored one.
func TestCheckReceivedName_NewRangesNameCases(t *testing.T) {
	for _, c := range namecases.Cases() {
		t.Run(c.Label, func(t *testing.T) {
			got, err := checkReceivedName(nil, c.In)
			if c.OK {
				if err != nil || got != c.Want {
					t.Fatalf("got %q, %v; want %q", got, err, c.Want)
				}
				return
			}
			if !errors.Is(err, ErrNameInvalidCharacters) {
				t.Fatalf("err = %v, want ErrNameInvalidCharacters", err)
			}
		})
	}
}

func TestCheckReceivedName_UnchangedStoredIsTolerated(t *testing.T) {
	bad, padded, other := "Co\u202ela", "  Co\u202ela ", "Co\u202dla"
	for _, tc := range []struct {
		label    string
		stored   string
		received string
		want     string
		refused  bool
	}{
		{"same bad name, received padded", bad, padded, bad, false},
		{"stored untrimmed", padded, padded, bad, false},
		{"different bad name", bad, other, "", true},
		{"changed to a good name", bad, "Cola", "Cola", false},
	} {
		t.Run(tc.label, func(t *testing.T) {
			stored := tc.stored
			got, err := checkReceivedName(&stored, tc.received)
			if tc.refused {
				if !errors.Is(err, ErrNameInvalidCharacters) {
					t.Fatalf("err = %v, want ErrNameInvalidCharacters", err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	if _, err := checkReceivedName(nil, bad); !errors.Is(err, ErrNameInvalidCharacters) {
		t.Fatalf("a new row with a bad name: err = %v", err)
	}
}
