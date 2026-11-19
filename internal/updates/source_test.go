package updates

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/universaltill/universal-till/internal/releasesource"
)

// failTransport fails the test on any request: a build with an unusable
// release source must not even reach the network (ut-docs#4080).
type failTransport struct{ t *testing.T }

func (f failTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.t.Errorf("unexpected outbound request to %s", r.URL)
	return nil, errors.New("blocked")
}

func stampSource(t *testing.T, raw string) {
	t.Helper()
	oldSrc, oldErr := buildSource, buildSourceErr
	buildSource, buildSourceErr = releasesource.Parse(raw)
	t.Cleanup(func() { buildSource, buildSourceErr = oldSrc, oldErr })
}

func TestReleasesURLFromEnv_DefaultIsExactLatest(t *testing.T) {
	t.Setenv("UT_UPDATE_RELEASES_URL", "")
	const want = "https://api.github.com/repos/universaltill/universal-till/releases/latest"
	if got := releasesURLFromEnv(); got != want {
		t.Fatalf("default check URL = %q, want %q", got, want)
	}
}

func TestNotesURLAllowed_StampedSource(t *testing.T) {
	stampSource(t, "https://api.github.com/repos/acme/till/releases")
	old := releasesURL
	t.Cleanup(func() { releasesURL = old })
	releasesURL = buildCheckURL()
	const acme = "https://github.com/acme/till/releases/download/v1/release-notes.json"
	const ours = "https://github.com/universaltill/universal-till/releases/download/v1/release-notes.json"
	if !notesURLAllowed(acme) {
		t.Error("stamped repo's notes refused")
	}
	if notesURLAllowed(ours) {
		t.Error("our repo's notes accepted by an acme build")
	}
	releasesURL = "http://127.0.0.1:9000/latest" // env override too
	if !notesURLAllowed("http://127.0.0.1:9000/release-notes.json") || !notesURLAllowed(acme) || notesURLAllowed(ours) {
		t.Error("with an env override: override host and stamped repo allowed, ours not")
	}
}

func TestInvalidStampedSource_NoCheck(t *testing.T) {
	resetState(t)
	t.Setenv("UT_UPDATE_RELEASES_URL", "")
	stampSource(t, "http://api.github.com/repos/acme/till/releases")
	if buildSourceErr == nil {
		t.Fatal("setup: expected an invalid source")
	}
	if got := releasesURLFromEnv(); got != "" {
		t.Fatalf("releasesURLFromEnv() = %q, want empty (fail closed)", got)
	}
	oldURL, oldClient := releasesURL, outboundClient
	t.Cleanup(func() { releasesURL, outboundClient = oldURL, oldClient })
	releasesURL = releasesURLFromEnv()
	outboundClient = &http.Client{Transport: failTransport{t}}
	if got := CheckNow(context.Background()); got != (Status{}) {
		t.Fatalf("status changed: %+v", got)
	}
	// A valid env override still works.
	t.Setenv("UT_UPDATE_RELEASES_URL", "https://mirror.example/latest")
	if got := releasesURLFromEnv(); got != "https://mirror.example/latest" {
		t.Fatalf("env override = %q", got)
	}
	if notesURLAllowed("https://github.com/universaltill/universal-till/releases/download/v1/x") {
		t.Error("an invalid stamp must not trust our GitHub for notes")
	}
}
