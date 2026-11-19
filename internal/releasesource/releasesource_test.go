package releasesource

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	defLatest = "https://api.github.com/repos/universaltill/universal-till/releases/latest"
	defAsset  = "https://github.com/universaltill/universal-till/releases/download/v1.2.3/checksums.txt"
)

func mustParse(t *testing.T, raw string) Source {
	t.Helper()
	s, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse(%q): %v", raw, err)
	}
	return s
}

func TestParseDefault(t *testing.T) {
	for _, raw := range []string{"", "  ", DefaultBase, DefaultBase + "/", DefaultBase + "/latest", DefaultBase + "/latest/"} {
		s := mustParse(t, raw)
		if got := s.LatestURL(); got != defLatest {
			t.Errorf("Parse(%q).LatestURL() = %q", raw, got)
		}
		if got, want := s.TagURL("1.2.3"), "https://api.github.com/repos/universaltill/universal-till/releases/tags/v1.2.3"; got != want {
			t.Errorf("Parse(%q).TagURL = %q", raw, got)
		}
	}
}

func TestParseAcmeGitHub(t *testing.T) {
	s := mustParse(t, "https://api.github.com/repos/acme/till/releases")
	if got, want := s.LatestURL(), "https://api.github.com/repos/acme/till/releases/latest"; got != want {
		t.Errorf("latest = %q", got)
	}
	if got, want := s.TagURL("2.0.0"), "https://api.github.com/repos/acme/till/releases/tags/v2.0.0"; got != want {
		t.Errorf("tag = %q", got)
	}
}

func TestParseNonGitHub(t *testing.T) {
	s := mustParse(t, "https://updates.acme.example/api/releases")
	if got, want := s.LatestURL(), "https://updates.acme.example/api/releases/latest"; got != want {
		t.Errorf("latest = %q", got)
	}
	if got, want := s.TagURL("1.2.3"), "https://updates.acme.example/api/releases/tags/v1.2.3"; got != want {
		t.Errorf("tag = %q", got)
	}
	ghe := mustParse(t, "https://ghe.example/api/v3/repos/O/R/releases")
	if got, want := ghe.LatestURL(), "https://ghe.example/api/v3/repos/O/R/releases/latest"; got != want {
		t.Errorf("ghe latest = %q", got)
	}
}

func TestParseRefusals(t *testing.T) {
	for _, raw := range []string{
		"ftp://x", "not a url", "http://api.github.com/repos/a/b/releases", "https://",
		"https://u:p@updates.acme.example/r", "https://updates.acme.example/r?q=1",
		"https://updates.acme.example/r#f", "https://updates.acme.example/a/../b",
		"https://api.github.com/repos/acme/releases", "https://api.github.com/repos/a/b/c/releases",
		"https://api.github.com:8443/repos/a/b/releases", "https://api.github.com/repos//b/releases",
		"https://github.com/acme/till/releases", "https://objects.githubusercontent.com/x",
		"https://raw.githubusercontent.com/x", "https://foo.github.com/x",
	} {
		_, err := Parse(raw)
		if !errors.Is(err, ErrInvalidBase) {
			t.Errorf("Parse(%q) err = %v, want ErrInvalidBase", raw, err)
		}
	}
}

func TestAssetTrustedDefault(t *testing.T) {
	s := mustParse(t, "")
	for _, ok := range []string{defAsset, "https://GITHUB.COM/universaltill/universal-till/releases/download/v1/x.tar.gz"} {
		if !s.AssetTrusted(ok) {
			t.Errorf("want trusted: %s", ok)
		}
	}
	for _, bad := range []string{
		"https://github.com/someone/else/releases/download/v1/x",
		"https://evil.example/universaltill/universal-till/releases/download/v1/x",
		"http://github.com/universaltill/universal-till/releases/download/v1/x",
		"https://github.com:443/universaltill/universal-till/releases/download/v1/x",
		"https://github.com/universaltill/universal-till/releases/downloadX/v1/x",
		"https://github.com/universaltill/universal-till/releases/download",
		"https://github.com/universaltill/universal-till/releases/download/v1/x?a=b",
		"https://github.com/universaltill/universal-till/releases/download/v1/x#f",
		"https://u@github.com/universaltill/universal-till/releases/download/v1/x",
		"https://github.com/universaltill/universal-till/releases/download/../x",
		"",
	} {
		if s.AssetTrusted(bad) {
			t.Errorf("want refused: %s", bad)
		}
	}
}

func TestAssetTrustedAcme(t *testing.T) {
	s := mustParse(t, "https://api.github.com/repos/acme/till/releases")
	if !s.AssetTrusted("https://github.com/acme/till/releases/download/v1/x") {
		t.Error("acme asset refused")
	}
	if s.AssetTrusted(defAsset) {
		t.Error("our asset trusted by acme build")
	}
}

func TestAssetTrustedGeneric(t *testing.T) {
	s := mustParse(t, "https://updates.acme.example/api/releases")
	if !s.AssetTrusted("https://updates.acme.example/any/path") {
		t.Error("same-host asset refused")
	}
	for _, bad := range []string{
		"http://updates.acme.example/any", "https://updates.acme.example:444/any", defAsset,
	} {
		if s.AssetTrusted(bad) {
			t.Errorf("want refused: %s", bad)
		}
	}
}

func TestZeroSourceTrustsNothing(t *testing.T) {
	var s Source
	if s.AssetTrusted(defAsset) || s.AssetTrusted("https://x.example/y") {
		t.Error("zero Source trusted something")
	}
}

func TestBuildIgnoresEnv(t *testing.T) {
	t.Setenv("UT_UPDATE_RELEASES_URL", "https://mirror.example/latest")
	got, err := Build()
	if err != nil {
		t.Fatal(err)
	}
	if want := mustParse(t, ""); got != want {
		t.Errorf("Build() = %+v, want %+v", got, want)
	}
}

func TestNoEnvAccessInPackage(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "os.Getenv") || strings.Contains(string(b), "os.LookupEnv") {
			t.Errorf("%s reads the environment; the release source is build-time only", f)
		}
	}
}

// Tester F1 (ut-docs#4080): a trailing-dot FQDN is the same host, so it must
// not slip past the GitHub-host refusal, and api.github.com. is the GitHub API.
func TestParseTrailingDotHost(t *testing.T) {
	for _, raw := range []string{
		"https://github.com./acme/till/releases",
		"https://GITHUB.COM./acme/till/releases",
		"https://objects.githubusercontent.com./x",
		"https://foo.github.com./x",
		"https://api.github.com.:8443/repos/a/b/releases",
		"https://api.github.com./repos/a/releases",
	} {
		if _, err := Parse(raw); !errors.Is(err, ErrInvalidBase) {
			t.Errorf("Parse(%q) err = %v, want ErrInvalidBase", raw, err)
		}
	}
	s := mustParse(t, "https://api.github.com./repos/acme/till/releases")
	if got, want := s.LatestURL(), "https://api.github.com/repos/acme/till/releases/latest"; got != want {
		t.Errorf("latest = %q, want %q", got, want)
	}
	if !s.AssetTrusted("https://github.com/acme/till/releases/download/v1/x") {
		t.Error("acme asset refused")
	}
}

// GitHub owner/repo names are case-insensitive and browser_download_url uses
// the canonical case, so a stamped "Acme/Till" must still accept acme/till.
func TestAssetTrustedGitHubOwnerCaseInsensitive(t *testing.T) {
	s := mustParse(t, "https://api.github.com/repos/Acme/Till/releases")
	if !s.AssetTrusted("https://github.com/acme/till/releases/download/v1/x") {
		t.Error("canonical-case asset refused")
	}
	if s.AssetTrusted("https://github.com/acme/other/releases/download/v1/x") {
		t.Error("other repo trusted")
	}
	g := mustParse(t, "https://updates.acme.example/api/releases")
	if !g.AssetTrusted("https://UPDATES.acme.example./Any") {
		t.Error("trailing-dot same host refused")
	}
}

// Tester F2: an empty query ("?", url.URL.ForceQuery) is a query too.
func TestCleanURLRefusesEmptyQuery(t *testing.T) {
	if _, ok := CleanURL(defAsset + "?"); ok {
		t.Error("CleanURL accepted a trailing empty query")
	}
	if mustParse(t, "").AssetTrusted(defAsset + "?") {
		t.Error("asset with empty query trusted")
	}
}
