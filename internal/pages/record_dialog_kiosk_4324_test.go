package pages

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ut-docs#4324: record_dialog.html renders its status/Lock/exit-to-OS row
// whatever the till's display.mode, because every page that uses it needs a
// staff session (TestCategoriesPage_RecordDialogStatusLockExitAffordance).
// Customer containment (coding-standards.md §10) therefore rests on the
// self-order kiosk's own templates never including the dialog. This pins it:
// a kiosk template that adopts the record dialog must bring its own
// containment instead of inheriting an operator affordance.
func TestSelfOrderKioskTemplatesHaveNoRecordDialog(t *testing.T) {
	var files []string
	for _, pattern := range []string{"web/ui/pages/self_order*.html", "web/ui/partials/self_order*.html"} {
		m, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, m...)
	}
	if len(files) < 2 {
		t.Fatalf("found only %d self-order templates (%v); has the kiosk moved?", len(files), files)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{`template "record_dialog"`, `data-record-dialog`} {
			if strings.Contains(string(b), banned) {
				t.Errorf("%s contains %s: the self-order kiosk faces customers and must not render the record dialog's Lock/exit-to-OS row (ut-docs#4324)", f, banned)
			}
		}
	}
}
