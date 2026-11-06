package plugins

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestDocsDoNotDescribeFreemiumPriceFlag pins ut-docs#5687: a marketplace
// listing is free or paid (ut-cloud's data model is a `paid_listing` bool),
// so the marketplace API copy and the plugin guidelines must not offer a
// third "freemium" price value or monetisation model.
func TestDocsDoNotDescribeFreemiumPriceFlag(t *testing.T) {
	freemium := regexp.MustCompile(`(?i)freemium`)
	for _, path := range []string{
		"../../docs/market_place_openapi.yaml",
		"../../docs/plugin_guidelines.md",
	} {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if freemium.MatchString(line) {
				t.Errorf("%s:%d mentions freemium; listings are free or paid only (ut-docs#5687): %s", path, i+1, strings.TrimSpace(line))
			}
		}
	}
}
