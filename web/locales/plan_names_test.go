package locales

import (
	"encoding/json"
	"io/fs"
	"strings"
	"testing"
)

// ut-docs#4738, ADR-0060 amendment 2026-10-17 point 2: plan names are
// product names, the same in every language. The paid-plan labels must
// equal en.json's, and the free plan's label must start with "Local"
// (only its "(free)" suffix is translated).
func TestPlanNamesAreNotTranslated(t *testing.T) {
	load := func(name string) map[string]string {
		b, err := fs.ReadFile(FS, name)
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]string{}
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return m
	}
	en := load("en.json")
	names, err := fs.Glob(FS, "*.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		m := load(name)
		for _, k := range []string{"subscription.plan.shop", "subscription.plan.pro", "subscription.plan.chain"} {
			if got, ok := m[k]; ok && got != en[k] {
				t.Errorf("%s: %s = %q, want the product name %q", name, k, got, en[k])
			}
		}
		if got, ok := m["subscription.plan.local"]; ok && !strings.HasPrefix(got, "Local") {
			t.Errorf("%s: subscription.plan.local = %q, want it to start with the product name \"Local\"", name, got)
		}
	}
}
