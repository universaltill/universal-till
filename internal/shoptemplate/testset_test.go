package shoptemplate

import "testing"

type permissive struct{}

func (permissive) HasKey(Kind, string) bool           { return true }
func (permissive) FixedClass(Kind, string) bool       { return false }
func (permissive) RegistryTier(string) (string, bool) { return "normal", true }
func (permissive) CapabilityKnown(string) bool        { return true }
func (permissive) EnKey(string) bool                  { return true }

// testSet is the in-package twin of shoptemplatetest.TestSet (which this
// package's own tests cannot import without a cycle).
func testSet(t testing.TB, ts ...*Template) *Set {
	t.Helper()
	for _, tm := range ts {
		if tm.LabelKey == "" {
			tm.LabelKey = "test.label"
		}
		if tm.SummaryKey == "" {
			tm.SummaryKey = "test.summary"
		}
	}
	s, err := Build(ts, permissive{}, false)
	if err != nil {
		t.Fatalf("shoptemplate.TestSet: %v", err)
	}
	return s
}
