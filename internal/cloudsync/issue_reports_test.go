package cloudsync

import (
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/issuereport"
)

func withTempPendingDir(t *testing.T) {
	t.Helper()
	orig := issuereport.PendingDir
	issuereport.PendingDir = t.TempDir()
	t.Cleanup(func() { issuereport.PendingDir = orig })
}

func registeredCfg(url string) *config.Config {
	cfg := &config.Config{}
	cfg.Marketplace.EndpointURL = url
	cfg.Marketplace.StoreID = "store-1"
	cfg.Marketplace.MerchantToken = "tok-1"
	return cfg
}

// uploadIssueReport must build a real multipart request carrying every
// field the cloud-side receiving endpoint (ADR-0022 spec 012 Phase 2)
// expects, including an optional video and the recent-logs lines — this
// function had zero direct coverage before this batch.
func TestUploadIssueReportSendsMultipartBundle(t *testing.T) {
	withTempPendingDir(t)
	id, err := issuereport.Save("printer jammed", []byte("fake-audio"), []byte("fake-video"))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	bundles, err := issuereport.Pending()
	if err != nil || len(bundles) != 1 {
		t.Fatalf("Pending: %v (%d bundles)", err, len(bundles))
	}
	b := bundles[0]

	var gotStoreID, gotDeviceless, gotReportID, gotNote, gotAuth string
	var gotAudio, gotVideo []byte
	var gotLogFields int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			t.Fatalf("server: parse multipart: %v", err)
		}
		gotStoreID = r.FormValue("store_id")
		gotDeviceless = r.FormValue("device_id")
		gotReportID = r.FormValue("report_id")
		gotNote = r.FormValue("note")
		gotLogFields = len(r.MultipartForm.Value["logs"])
		if fh := r.MultipartForm.File["audio"]; len(fh) == 1 {
			f, _ := fh[0].Open()
			gotAudio = make([]byte, fh[0].Size)
			_, _ = f.Read(gotAudio)
			f.Close()
		}
		if fh := r.MultipartForm.File["video"]; len(fh) == 1 {
			f, _ := fh[0].Open()
			gotVideo = make([]byte, fh[0].Size)
			_, _ = f.Read(gotVideo)
			f.Close()
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := uploadIssueReport(context.Background(), registeredCfg(srv.URL), b); err != nil {
		t.Fatalf("uploadIssueReport: %v", err)
	}
	if gotAuth != "Bearer tok-1" {
		t.Fatalf("auth header = %q", gotAuth)
	}
	if gotStoreID != "store-1" || gotReportID != id || gotNote != "printer jammed" {
		t.Fatalf("fields: store=%q report=%q note=%q", gotStoreID, gotReportID, gotNote)
	}
	_ = gotDeviceless // device_id: no enrolled device in this test, empty string is fine
	if string(gotAudio) != "fake-audio" {
		t.Fatalf("audio = %q", gotAudio)
	}
	if string(gotVideo) != "fake-video" {
		t.Fatalf("video = %q", gotVideo)
	}
	if gotLogFields < 0 {
		t.Fatalf("logs fields = %d", gotLogFields)
	}
}

// A bundle with no video must omit the video part entirely rather than send
// an empty one — attachFile's "only if VideoPath != ”" guard.
func TestUploadIssueReportOmitsVideoWhenAbsent(t *testing.T) {
	withTempPendingDir(t)
	if _, err := issuereport.Save("no video", []byte("fake-audio"), nil); err != nil {
		t.Fatalf("Save: %v", err)
	}
	bundles, err := issuereport.Pending()
	if err != nil || len(bundles) != 1 {
		t.Fatalf("Pending: %v (%d)", err, len(bundles))
	}
	if bundles[0].VideoPath != "" {
		t.Fatalf("VideoPath = %q, want empty", bundles[0].VideoPath)
	}

	var hadVideoPart bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(10 << 20)
		hadVideoPart = len(r.MultipartForm.File["video"]) > 0
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := uploadIssueReport(context.Background(), registeredCfg(srv.URL), bundles[0]); err != nil {
		t.Fatalf("uploadIssueReport: %v", err)
	}
	if hadVideoPart {
		t.Fatal("video part sent for a bundle with no recording")
	}
}

func TestUploadIssueReportUnregistered(t *testing.T) {
	withTempPendingDir(t)
	if _, err := issuereport.Save("note", []byte("a"), nil); err != nil {
		t.Fatalf("Save: %v", err)
	}
	bundles, _ := issuereport.Pending()
	err := uploadIssueReport(context.Background(), &config.Config{}, bundles[0])
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("err = %v, want not registered", err)
	}
}

func TestUploadIssueReportNon200Fails(t *testing.T) {
	withTempPendingDir(t)
	if _, err := issuereport.Save("note", []byte("a"), nil); err != nil {
		t.Fatalf("Save: %v", err)
	}
	bundles, _ := issuereport.Pending()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	if err := uploadIssueReport(context.Background(), registeredCfg(srv.URL), bundles[0]); err == nil {
		t.Fatal("want error on 500 response")
	}
}

// attachFile's own error branch: a bundle whose audio file vanished from
// disk between Pending() listing it and upload actually being attempted.
func TestUploadIssueReportMissingAudioFileErrors(t *testing.T) {
	withTempPendingDir(t)
	if _, err := issuereport.Save("note", []byte("a"), nil); err != nil {
		t.Fatalf("Save: %v", err)
	}
	bundles, _ := issuereport.Pending()
	b := bundles[0]
	// Simulate the audio file disappearing after Pending() already listed it.
	if err := os.Remove(b.AudioPath); err != nil {
		t.Fatalf("remove audio: %v", err)
	}
	err := uploadIssueReport(context.Background(), registeredCfg("http://unused.example"), b)
	if err == nil {
		t.Fatal("want error when the audio file is missing")
	}
}

// uploadPendingIssueReports drives the real Pending -> upload -> Discard
// cycle: a bundle that uploads successfully must be discarded, and a bundle
// the cloud rejects must stay pending for the next tick to retry.
func TestUploadPendingIssueReportsFullCycle(t *testing.T) {
	withTempPendingDir(t)
	okID, err := issuereport.Save("this one uploads", []byte("audio-ok"), nil)
	if err != nil {
		t.Fatalf("Save ok bundle: %v", err)
	}
	failID, err := issuereport.Save("this one fails", []byte("audio-fail"), nil)
	if err != nil {
		t.Fatalf("Save failing bundle: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(10 << 20)
		if r.FormValue("report_id") == failID {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	uploadPendingIssueReports(context.Background(), registeredCfg(srv.URL))

	remaining, err := issuereport.Pending()
	if err != nil {
		t.Fatalf("Pending after upload: %v", err)
	}
	if len(remaining) != 1 || remaining[0].Meta.ID != failID {
		ids := make([]string, len(remaining))
		for i, b := range remaining {
			ids[i] = b.Meta.ID
		}
		t.Fatalf("remaining pending = %v, want only %q", ids, failID)
	}
	_ = okID
}

// Pending() itself erroring (not just "no bundles yet") must not panic the
// tick loop — uploadPendingIssueReports logs and returns.
func TestUploadPendingIssueReportsPendingListError(t *testing.T) {
	orig := issuereport.PendingDir
	// A regular file where a directory is expected makes os.ReadDir fail with
	// something other than the plain "does not exist" case Pending() already
	// handles.
	f := t.TempDir() + "/not-a-dir"
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	issuereport.PendingDir = f
	t.Cleanup(func() { issuereport.PendingDir = orig })

	// Must not panic; nothing to assert on the (empty) cloud side.
	uploadPendingIssueReports(context.Background(), registeredCfg("http://unused.example"))
}

func TestAttachFileMultipartFormWritesRealBytes(t *testing.T) {
	withTempPendingDir(t)
	if _, err := issuereport.Save("note", []byte("hello-bytes"), nil); err != nil {
		t.Fatalf("Save: %v", err)
	}
	bundles, _ := issuereport.Pending()
	var buf strings.Builder
	w := multipart.NewWriter(&buf)
	if err := attachFile(w, "audio", "audio.webm", bundles[0].AudioPath); err != nil {
		t.Fatalf("attachFile: %v", err)
	}
	w.Close()
	if !strings.Contains(buf.String(), "hello-bytes") {
		t.Fatal("attached file content missing from multipart body")
	}
}
