// Package packaging holds nothing but pos.env.example, the template stamped
// into every release artifact (.goreleaser.yaml, macos/build-app.sh). Its
// UT_MARKETPLACE_ENDPOINT_URL default is never read at runtime — only
// packaged — so a stale value here fails silently: sales keep working
// offline, but the plugin store and cloud sync quietly point at whatever
// domain shipped, with no error the operator would ever see.
package packaging

import (
	"os"
	"strings"
	"testing"
)

func TestPosEnvExampleDefaultsToCanonicalCloudHost(t *testing.T) {
	data, err := os.ReadFile("pos.env.example")
	if err != nil {
		t.Fatalf("read pos.env.example: %v", err)
	}
	const want = "UT_MARKETPLACE_ENDPOINT_URL=https://cloud.universaltill.com/api"
	if !strings.Contains(string(data), want) {
		t.Fatalf("pos.env.example missing the canonical default line %q — every new till install picks up whatever this file says", want)
	}
}
