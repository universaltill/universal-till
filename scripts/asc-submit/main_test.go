package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeASC serves canned App Store Connect JSON (fixtures, no network) and
// records every request as "METHOD /path" plus its body.
type fakeASC struct {
	t      *testing.T
	routes map[string]string // "METHOD /path" -> JSON response
	calls  []string
	bodies map[string]string
	srv    *httptest.Server
}

func newFake(t *testing.T, routes map[string]string) *fakeASC {
	f := &fakeASC{t: t, routes: routes, bodies: map[string]string{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		b, _ := io.ReadAll(r.Body)
		f.calls = append(f.calls, key)
		f.bodies[key] = string(b)
		resp, ok := f.routes[key]
		if !ok {
			http.Error(w, `{"errors":[{"detail":"no fixture for `+key+`"}]}`, http.StatusNotFound)
			return
		}
		w.WriteHeader(map[string]int{"POST": 201}[r.Method] + map[bool]int{true: 0, false: 200}[r.Method == "POST"])
		_, _ = w.Write([]byte(resp))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeASC) writes() []string {
	var w []string
	for _, c := range f.calls {
		if !strings.HasPrefix(c, "GET ") {
			w = append(w, c)
		}
	}
	return w
}

func (f *fakeASC) client() *client {
	return &client{base: f.srv.URL, http: f.srv.Client(), token: func() (string, error) { return "tok", nil }, sleep: func(time.Duration) {}}
}

func openedFile(t *testing.T, content string) string {
	p := filepath.Join(t.TempDir(), "apple.opened")
	if content != "" {
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

const (
	appJSON   = `{"data":[{"type":"apps","id":"APP1","attributes":{"bundleId":"com.universaltill.pos"}}]}`
	buildOK   = `{"data":[{"type":"builds","id":"B1","attributes":{"processingState":"VALID","version":"63.1"}}]}`
	buildBad  = `{"data":[{"type":"builds","id":"B1","attributes":{"processingState":"PROCESSING","version":"63.1"}}]}`
	verOpen   = `{"data":[{"type":"appStoreVersions","id":"V1","attributes":{"versionString":"0.36.1","appStoreState":"PREPARE_FOR_SUBMISSION"}}]}`
	noData    = `{"data":[]}`
	phasedHas = `{"data":{"type":"appStoreVersionPhasedReleases","id":"P1","attributes":{"phasedReleaseState":"INACTIVE"}}}`
)

func baseRoutes(builds, versions string) map[string]string {
	return map[string]string{
		"GET /v1/apps":                                             appJSON,
		"GET /v1/builds":                                           builds,
		"GET /v1/apps/APP1/appStoreVersions":                       versions,
		"PATCH /v1/appStoreVersions/V1":                            `{"data":{"type":"appStoreVersions","id":"V1"}}`,
		"PATCH /v1/appStoreVersions/V1/relationships/build":        `{}`,
		"POST /v1/appStoreVersions":                                `{"data":{"type":"appStoreVersions","id":"V2"}}`,
		"PATCH /v1/appStoreVersions/V2/relationships/build":        `{}`,
		"GET /v1/appStoreVersions/V1/appStoreVersionPhasedRelease": noData,
		"GET /v1/appStoreVersions/V2/appStoreVersionPhasedRelease": noData,
		"POST /v1/appStoreVersionPhasedReleases":                   `{"data":{"type":"appStoreVersionPhasedReleases","id":"P1"}}`,
		"GET /v1/reviewSubmissions":                                noData,
		"POST /v1/reviewSubmissions":                               `{"data":{"type":"reviewSubmissions","id":"R1"}}`,
		"POST /v1/reviewSubmissionItems":                           `{"data":{"type":"reviewSubmissionItems","id":"I1"}}`,
		"PATCH /v1/reviewSubmissions/R1":                           `{"data":{"type":"reviewSubmissions","id":"R1"}}`,
		"GET /v1/reviewSubmissions/R1/items":                       noData,
	}
}

func cfg(dry bool, opened string) config {
	return config{BundleID: "com.universaltill.pos", Version: "0.36.2", Build: "63.1", DryRun: dry, OpenedPath: opened}
}

func TestDryRunNeverWrites(t *testing.T) {
	f := newFake(t, baseRoutes(buildOK, verOpen))
	var out bytes.Buffer
	if err := run(context.Background(), cfg(true, openedFile(t, "")), f.client(), &out); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if w := f.writes(); len(w) != 0 {
		t.Fatalf("dry run wrote: %v", w)
	}
	for _, want := range []string{"would set versionString 0.36.1 -> 0.36.2", "would attach build 63.1", "would create the phased release", "would submit"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("dry-run output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestRealRunReusesOpenVersionAndSubmits(t *testing.T) {
	f := newFake(t, baseRoutes(buildOK, verOpen))
	var out bytes.Buffer
	opened := openedFile(t, "opened by the owner, ut-docs#6414\n")
	if err := run(context.Background(), cfg(false, opened), f.client(), &out); err != nil {
		t.Fatalf("run: %v", err)
	}
	want := []string{
		"PATCH /v1/appStoreVersions/V1",
		"PATCH /v1/appStoreVersions/V1/relationships/build",
		"POST /v1/appStoreVersionPhasedReleases",
		"POST /v1/reviewSubmissions",
		"POST /v1/reviewSubmissionItems",
		"PATCH /v1/reviewSubmissions/R1",
	}
	if got := f.writes(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("writes\n got %v\nwant %v", got, want)
	}
	if strings.Contains(strings.Join(f.writes(), " "), "POST /v1/appStoreVersions ") {
		t.Fatal("created a second version instead of reusing the open one")
	}
	var b struct {
		Data struct {
			Attributes map[string]any `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(f.bodies["PATCH /v1/appStoreVersions/V1"]), &b); err != nil || b.Data.Attributes["versionString"] != "0.36.2" {
		t.Fatalf("PATCH body %q does not set versionString 0.36.2", f.bodies["PATCH /v1/appStoreVersions/V1"])
	}
	if !strings.Contains(f.bodies["POST /v1/appStoreVersionPhasedReleases"], `"INACTIVE"`) {
		t.Fatalf("phased release not created INACTIVE (starts on release): %s", f.bodies["POST /v1/appStoreVersionPhasedReleases"])
	}
	if !strings.Contains(f.bodies["PATCH /v1/reviewSubmissions/R1"], `"submitted":true`) {
		t.Fatalf("submission not submitted: %s", f.bodies["PATCH /v1/reviewSubmissions/R1"])
	}
}

func TestRealRunCreatesVersionWhenNoneOpenWithManualRelease(t *testing.T) {
	r := baseRoutes(buildOK, noData)
	r["GET /v1/appStoreVersions/V2/appStoreVersionPhasedRelease"] = phasedHas
	f := newFake(t, r)
	if err := run(context.Background(), cfg(false, openedFile(t, "ut-docs#6414")), f.client(), io.Discard); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(f.bodies["POST /v1/appStoreVersions"], `"MANUAL"`) {
		t.Fatalf("new version must be a manual release: %s", f.bodies["POST /v1/appStoreVersions"])
	}
	for _, w := range f.writes() {
		if w == "POST /v1/appStoreVersionPhasedReleases" {
			t.Fatal("created a phased release although one exists")
		}
	}
}

func TestBuildNotValidFailsBeforeAnyWrite(t *testing.T) {
	f := newFake(t, baseRoutes(buildBad, verOpen))
	err := run(context.Background(), cfg(false, openedFile(t, "ut-docs#6414")), f.client(), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "PROCESSING") {
		t.Fatalf("want a not-VALID error, got %v", err)
	}
	if w := f.writes(); len(w) != 0 {
		t.Fatalf("wrote despite an unusable build: %v", w)
	}
}

func TestRealRunRefusedWhileStoreNotOpened(t *testing.T) {
	for name, content := range map[string]string{"missing file": "", "no card": "opened"} {
		f := newFake(t, baseRoutes(buildOK, verOpen))
		err := run(context.Background(), cfg(false, openedFile(t, content)), f.client(), io.Discard)
		if err == nil || !strings.Contains(err.Error(), "apple.opened") {
			t.Fatalf("%s: want a refusal naming apple.opened, got %v", name, err)
		}
		if len(f.calls) != 0 {
			t.Fatalf("%s: made API calls before the opening check: %v", name, f.calls)
		}
	}
}

func TestExistingOpenSubmissionIsReused(t *testing.T) {
	r := baseRoutes(buildOK, verOpen)
	r["GET /v1/reviewSubmissions"] = `{"data":[{"type":"reviewSubmissions","id":"R1","attributes":{"state":"READY_FOR_REVIEW"}}]}`
	r["GET /v1/appStoreVersions/V1/appStoreVersionPhasedRelease"] = phasedHas
	f := newFake(t, r)
	if err := run(context.Background(), cfg(false, openedFile(t, "ut-docs#6414")), f.client(), io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, w := range f.writes() {
		if w == "POST /v1/reviewSubmissions" {
			t.Fatal("opened a second review submission")
		}
	}
}

func TestErrorBodyIsOneCappedLine(t *testing.T) {
	f := newFake(t, map[string]string{})
	err := run(context.Background(), cfg(true, ""), f.client(), io.Discard)
	if err == nil || strings.Contains(err.Error(), "\n") {
		t.Fatalf("want a one-line error, got %v", err)
	}
}

func TestRejectedSubmissionIsReusedAndItemNotAddedTwice(t *testing.T) {
	r := baseRoutes(buildOK, verOpen)
	r["GET /v1/reviewSubmissions"] = `{"data":[{"type":"reviewSubmissions","id":"R1","attributes":{"state":"UNRESOLVED_ISSUES"}}]}`
	r["GET /v1/reviewSubmissions/R1/items"] = `{"data":[{"type":"reviewSubmissionItems","id":"I1","relationships":{"appStoreVersion":{"data":{"type":"appStoreVersions","id":"V1"}}}}]}`
	f := newFake(t, r)
	if err := run(context.Background(), cfg(false, openedFile(t, "ut-docs#6414")), f.client(), io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, w := range f.writes() {
		if w == "POST /v1/reviewSubmissions" || w == "POST /v1/reviewSubmissionItems" {
			t.Fatalf("repeated %s", w)
		}
	}
	if !strings.Contains(strings.Join(f.writes(), " "), "PATCH /v1/reviewSubmissions/R1") {
		t.Fatal("did not submit the reused submission")
	}
}

func TestPhasedReleaseRefusalDoesNotBlockSubmit(t *testing.T) {
	r := baseRoutes(buildOK, verOpen)
	delete(r, "POST /v1/appStoreVersionPhasedReleases") // fixture answers 404
	f := newFake(t, r)
	var out bytes.Buffer
	if err := run(context.Background(), cfg(false, openedFile(t, "ut-docs#6414")), f.client(), &out); err != nil {
		t.Fatalf("a refused phased release must only warn: %v", err)
	}
	if !strings.Contains(out.String(), "::warning::") {
		t.Fatalf("no warning printed:\n%s", out.String())
	}
}
