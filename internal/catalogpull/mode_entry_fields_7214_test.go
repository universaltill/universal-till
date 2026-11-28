package catalogpull

import (
	"reflect"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#7214: the field list data.otherFieldsMatchBase compares at mode
// entry is pinned to uploadFields, so a new upload field cannot be silently
// left uncompared.
func TestModeEntryCompareFieldsMatchUploadFields_7214(t *testing.T) {
	for entity, got := range data.ModeEntryCompareFields {
		want, ok := uploadFields[entity]
		if !ok {
			t.Fatalf("ModeEntryCompareFields has %q, not an uploadFields kind", entity)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: ModeEntryCompareFields = %v, uploadFields = %v", entity, got, want)
		}
	}
	for _, kind := range []string{kindItem, kindCategory} {
		if _, ok := data.ModeEntryCompareFields[kind]; !ok {
			t.Errorf("ModeEntryCompareFields lacks %q", kind)
		}
	}
	for _, p := range data.PromotedUploadFieldKeys {
		fields, ok := data.ModeEntryCompareFields[p[0]]
		if !ok || !contains(fields, p[1]) {
			t.Errorf("promoted %v is not in ModeEntryCompareFields", p)
		}
	}
}
