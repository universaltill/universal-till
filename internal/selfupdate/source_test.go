package selfupdate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/releasesource"
)

// recordingTransport remembers every URL requested through it.
type recordingTransport struct {
	next http.RoundTripper
	mu   sync.Mutex
	urls []string
}

func (r *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.urls = append(r.urls, req.URL.String())
	r.mu.Unlock()
	return r.next.RoundTrip(req)
}

func (r *recordingTransport) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.urls...)
}

// useReleaseSource points the self-updater at a TLS test server: the release
// source is `base` (parsed like a stamped buildinfo value) and outboundClient
// trusts srv's certificate. It returns a recorder of every outbound request.
func useReleaseSource(t *testing.T, srv *httptest.Server, base string) *recordingTransport {
	t.Helper()
	oldSrc, oldClient := releaseSource, outboundClient
	t.Cleanup(func() { releaseSource, outboundClient = oldSrc, oldClient })
	releaseSource = func() (releasesource.Source, error) { return releasesource.Parse(base) }
	rec := &recordingTransport{next: srv.Client().Transport}
	outboundClient = &http.Client{Transport: rec}
	return rec
}

// requireOnlyMetadata fails if anything but release-metadata GETs went out.
func requireOnlyMetadata(t *testing.T, rec *recordingTransport) {
	t.Helper()
	for _, u := range rec.seen() {
		if !strings.HasSuffix(u, "/latest") && !strings.Contains(u, "/tags/") {
			t.Errorf("an asset was requested before the trust check: %s", u)
		}
	}
}

func TestRequireTrusted(t *testing.T) {
	src, err := releasesource.Parse("https://api.github.com/repos/acme/till/releases")
	if err != nil {
		t.Fatal(err)
	}
	r := &ghRelease{src: src}
	const ok = "https://github.com/acme/till/releases/download/v1/x"
	if err := r.requireTrusted("", ok, ""); err != nil {
		t.Errorf("empty and trusted URLs: %v", err)
	}
	const bad = "https://github.com/universaltill/universal-till/releases/download/v1/x"
	err = r.requireTrusted(ok, bad, "https://evil.example/y")
	if !errors.Is(err, releasesource.ErrUntrustedAsset) || !strings.Contains(err.Error(), bad) {
		t.Errorf("err = %v, want ErrUntrustedAsset naming the first untrusted URL", err)
	}
	if (&ghRelease{}).requireTrusted(ok) == nil {
		t.Error("a release with no source trusts nothing")
	}
}

func TestFetchReleaseInvalidSourceMakesNoRequest(t *testing.T) {
	oldSrc, oldClient := releaseSource, outboundClient
	t.Cleanup(func() { releaseSource, outboundClient = oldSrc, oldClient })
	releaseSource = func() (releasesource.Source, error) {
		return releasesource.Parse("http://api.github.com/repos/acme/till/releases")
	}
	outboundClient = &http.Client{Transport: failRoundTrip{t}}
	_, err := fetchRelease(context.Background(), "")
	if !errors.Is(err, releasesource.ErrInvalidBase) {
		t.Fatalf("err = %v, want ErrInvalidBase", err)
	}
	err = ApplyVersion(context.Background(), "0.2.0")
	if !errors.Is(err, releasesource.ErrInvalidBase) {
		t.Fatalf("ApplyVersion err = %v, want ErrInvalidBase", err)
	}
}

type failRoundTrip struct{ t *testing.T }

func (f failRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) {
	f.t.Errorf("unexpected outbound request to %s", r.URL)
	return nil, errors.New("blocked")
}

// The release source is build-time only: UT_UPDATE_RELEASES_URL (the check's
// override) must not steer the metadata fetch or the trusted asset host.
func TestEnvOverrideDoesNotMoveDownload(t *testing.T) {
	envSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("env-override server was contacted: %s", r.URL)
	}))
	t.Cleanup(envSrv.Close)
	t.Setenv("UT_UPDATE_RELEASES_URL", envSrv.URL+"/latest")
	oldVer := buildinfo.Version
	buildinfo.Version = "0.1.0"
	t.Cleanup(func() { buildinfo.Version = oldVer })

	rs := newReleaseServer(t, "v0.2.0", map[string][]byte{"checksums.txt": []byte("x")})
	rel, err := fetchRelease(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := rs.requested(); len(got) != 1 || got[0] != "/latest" {
		t.Errorf("metadata paths = %v", got)
	}
	if err := rel.requireTrusted(envSrv.URL + "/asset/checksums.txt"); !errors.Is(err, releasesource.ErrUntrustedAsset) {
		t.Errorf("an asset on the env override host: err = %v, want ErrUntrustedAsset", err)
	}
	if err := rel.requireTrusted(rs.srv.URL + "/asset/checksums.txt"); err != nil {
		t.Errorf("asset on the build source host refused: %v", err)
	}
}

// Linux/Pi tar.gz: an archive or checksums.txt off the trusted host is refused
// before anything is downloaded, and the install is untouched.
func TestApplyRefusesUntrustedAssetsLinux(t *testing.T) {
	name := archiveNameFor("0.2.0")
	archive := makeTarGz(t, []tarEntry{{name: "unitill-pos", body: "NEW"}})
	sums := []byte(sha256hex(archive) + "  " + name + "\n")
	const gh = "https://github.com/universaltill/universal-till/releases/download/v0.2.0/"
	for label, urls := range map[string]map[string]string{
		"archive":   {name: gh + name},
		"checksums": {"checksums.txt": "https://evil.example/checksums.txt"},
	} {
		t.Run(label, func(t *testing.T) {
			fix := newInstallFixture(t)
			rs := newReleaseServer(t, "v0.2.0", map[string][]byte{name: archive, "checksums.txt": sums})
			rs.urls = urls
			rec := useReleaseSource(t, rs.srv, rs.srv.URL)
			err := Apply(context.Background())
			if !errors.Is(err, releasesource.ErrUntrustedAsset) {
				t.Fatalf("err = %v, want ErrUntrustedAsset", err)
			}
			requireOnlyMetadata(t, rec)
			if rs.assetHits != 0 {
				t.Errorf("asset requests = %d, want 0", rs.assetHits)
			}
			if got, _ := os.ReadFile(fix.exe); string(got) != fix.oldBin {
				t.Errorf("binary changed: %q", got)
			}
			if _, err := os.Stat(fix.exe + ".bak"); err == nil {
				t.Error("a backup was made: the swap started")
			}
		})
	}
}

// Windows installer path.
func TestApplyRefusesUntrustedAssetsWindows(t *testing.T) {
	setup := []byte("SIGNED-SETUP-v2")
	const gh = "https://github.com/universaltill/universal-till/releases/download/v0.2.0/"
	for label, urls := range map[string]map[string]string{
		"setup":     {winSetup: gh + winSetup},
		"checksums": {"checksums.txt": "https://evil.example/checksums.txt"},
	} {
		t.Run(label, func(t *testing.T) {
			f := newWinFixture(t)
			rs := newReleaseServer(t, "v0.2.0", winAssets(setup))
			rs.urls = urls
			rec := useReleaseSource(t, rs.srv, rs.srv.URL)
			err := Apply(context.Background())
			if !errors.Is(err, releasesource.ErrUntrustedAsset) {
				t.Fatalf("err = %v, want ErrUntrustedAsset", err)
			}
			requireOnlyMetadata(t, rec)
			if rs.assetHits != 0 {
				t.Errorf("asset requests = %d, want 0", rs.assetHits)
			}
			if m, _ := filepath.Glob(filepath.Join(f.work, "attempt-*")); len(m) != 0 {
				t.Errorf("an attempt folder was created: %v", m)
			}
			f.noHelper(t)
			if got, _ := os.ReadFile(f.exe); string(got) != "OLD" {
				t.Errorf("binary changed: %q", got)
			}
		})
	}
}
