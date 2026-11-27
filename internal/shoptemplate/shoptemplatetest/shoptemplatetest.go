// Package shoptemplatetest builds shoptemplate Sets from literal templates
// for other packages' tests. It lives in its own package, imported only by
// tests, so the production binary (and the whole-program deadcode baseline,
// guard-deadcode-baseline.sh) never sees it.
package shoptemplatetest

import "github.com/universaltill/universal-till/internal/shoptemplate"

// TB is the slice of testing.TB TestSet needs.
type TB interface {
	Helper()
	Fatalf(format string, args ...any)
}

type permissive struct{}

func (permissive) HasKey(shoptemplate.Kind, string) bool     { return true }
func (permissive) FixedClass(shoptemplate.Kind, string) bool { return false }
func (permissive) RegistryTier(string) (string, bool)        { return "normal", true }
func (permissive) CapabilityKnown(string) bool               { return true }
func (permissive) EnKey(string) bool                         { return true }

// TestSet builds a Set from literal templates (G4 settings demotion, G5
// menu): extends/seed/class rules still apply, but row, key and en.json
// existence are not checked and not every shop type needs a template.
func TestSet(t TB, ts ...*shoptemplate.Template) *shoptemplate.Set {
	t.Helper()
	for _, tm := range ts {
		if tm.LabelKey == "" {
			tm.LabelKey = "test.label"
		}
		if tm.SummaryKey == "" {
			tm.SummaryKey = "test.summary"
		}
	}
	s, err := shoptemplate.Build(ts, permissive{}, false)
	if err != nil {
		t.Fatalf("shoptemplatetest.TestSet: %v", err)
	}
	return s
}
