package marketplace

import (
	"context"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/updates"
)

// ut-docs#2700: the Makefile's VERSION default is what every `make build` /
// `make run` binary reports. It must (a) not be "dev" (ut-docs#369: updates
// treats "dev" as older than every release, so auto-update would replace a
// fresh build), yet (b) not be a bare release number, or the catalog would
// send host_version=<it> and locally hide listings whose min_host_version is
// newer than a build that is really tip-of-main.

var makefileVersionDefault = regexp.MustCompile(`(?m)^VERSION\?=(\S*)\s*$`)

func makefileDefaultVersion(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../../Makefile")
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	m := makefileVersionDefault.FindSubmatch(b)
	if m == nil {
		t.Fatal("Makefile has no `VERSION?=<default>` line")
	}
	return string(m[1])
}

func TestMakefileDefaultVersion_IsDevPrerelease(t *testing.T) {
	def := makefileDefaultVersion(t)
	if def == "" || def == "dev" {
		t.Fatalf("Makefile VERSION default = %q; must not be empty or \"dev\" (ut-docs#369)", def)
	}
	if !strings.HasSuffix(def, "-dev") {
		t.Errorf("Makefile VERSION default = %q, want a \"-dev\" prerelease", def)
	}
	if got := releaseVersion(def); got != "" {
		t.Errorf("releaseVersion(%q) = %q, want \"\" so host_version is omitted (ut-docs#2700)", def, got)
	}
}

func TestMakefileDefaultVersion_KeepsEveryListing(t *testing.T) {
	def := makefileDefaultVersion(t)
	withHostVersion(t, def)
	var q url.Values
	c := compatTestClient(t, []PluginSummary{{ID: "future", MinHostVersion: "99.0.0"}}, &q)

	resp, err := c.ListPlugins(context.Background(), &ListPluginsRequest{})
	if err != nil {
		t.Fatalf("ListPlugins: %v", err)
	}
	if q.Has("host_version") {
		t.Errorf("host_version sent (%q) for a make-build binary, want omitted", q.Get("host_version"))
	}
	if len(resp.Plugins) != 1 || resp.Plugins[0].ID != "future" {
		t.Errorf("plugins = %+v, want the min_host_version 99.0.0 listing kept", resp.Plugins)
	}
}

func TestMakefileDefaultVersion_StillOlderThanReleases(t *testing.T) {
	def := makefileDefaultVersion(t)
	for _, rel := range []string{"0.1.0", "0.31.0"} {
		if !updates.Newer(rel, def) {
			t.Errorf("updates.Newer(%q, %q) = false; a make-build binary must never outrank a real release", rel, def)
		}
	}
}
