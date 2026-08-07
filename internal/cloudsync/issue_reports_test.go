package cloudsync

import (
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/issuereport"
	"github.com/universaltill/universal-till/internal/logging"
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
//
// The bundle is built directly (not via issuereport.Save, which pulls
// Meta.Logs from the process-wide logging.Recent() ring buffer — shared,
// mutable, order-dependent across the whole test binary) so Logs and
// CreatedAt are known values this test can actually assert against,
// instead of merely asserting a field is present.
func TestUploadIssueReportSendsMultipartBundle(t *testing.T) {
	dir := t.TempDir()
	audioPath := filepath.Join(dir, "audio.webm")
	if err := os.WriteFile(audioPath, []byte("fake-audio"), 0o644); err != nil {
		t.Fatalf("write audio fixture: %v", err)
	}
	videoPath := filepath.Join(dir, "video.webm")
	if err := os.WriteFile(videoPath, []byte("fake-video"), 0o644); err != nil {
		t.Fatalf("write video fixture: %v", err)
	}
	createdAt := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	b := issuereport.Bundle{
		Meta: issuereport.Meta{
			ID:        "report-123",
			Note:      "printer jammed",
			Locale:    "fa",
			CreatedAt: createdAt,
			Logs: []logging.Problem{
				{At: createdAt, Level: "WARN", Msg: "printer offline"},
				{At: createdAt, Level: "ERROR", Msg: "paper jam sensor tripped"},
			},
		},
		Dir:       dir,
		AudioPath: audioPath,
		VideoPath: videoPath,
	}

	var gotStoreID, gotReportID, gotNote, gotAuth, gotCreatedAt, gotLocale string
	var gotAudio, gotVideo []byte
	var gotLogs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			t.Errorf("server: parse multipart: %v", err)
			return
		}
		gotStoreID = r.FormValue("store_id")
		gotReportID = r.FormValue("report_id")
		gotNote = r.FormValue("note")
		gotLocale = r.FormValue("locale")
		gotCreatedAt = r.FormValue("created_at")
		gotLogs = append([]string(nil), r.MultipartForm.Value["logs"]...)
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
	if gotStoreID != "store-1" || gotReportID != "report-123" || gotNote != "printer jammed" {
		t.Fatalf("fields: store=%q report=%q note=%q", gotStoreID, gotReportID, gotNote)
	}
	if gotLocale != "fa" {
		t.Fatalf("locale = %q, want %q (ut-docs#397 — Whisper needs the capture-time UI locale)", gotLocale, "fa")
	}
	if gotCreatedAt != createdAt.Format("2006-01-02T15:04:05.000000000Z07:00") {
		t.Fatalf("created_at = %q", gotCreatedAt)
	}
	if string(gotAudio) != "fake-audio" {
		t.Fatalf("audio = %q", gotAudio)
	}
	if string(gotVideo) != "fake-video" {
		t.Fatalf("video = %q", gotVideo)
	}
	wantLogs := []string{
		createdAt.Format("2006-01-02T15:04:05Z07:00") + "\tWARN\tprinter offline",
		createdAt.Format("2006-01-02T15:04:05Z07:00") + "\tERROR\tpaper jam sensor tripped",
	}
	if len(gotLogs) != len(wantLogs) || gotLogs[0] != wantLogs[0] || gotLogs[1] != wantLogs[1] {
		t.Fatalf("logs = %v, want %v", gotLogs, wantLogs)
	}
}

// A bundle with screenshots must send each as its own "image" multipart
// part (repeated field name), in ImagePaths order — the cloud side reads
// them back as a slice.
func TestUploadIssueReportSendsMultipleImageParts(t *testing.T) {
	dir := t.TempDir()
	var imagePaths []string
	imageContents := []string{"fake-png-0", "fake-png-1", "fake-png-2"}
	for i, content := range imageContents {
		p := filepath.Join(dir, fmt.Sprintf("image-%d.png", i))
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write image fixture: %v", err)
		}
		imagePaths = append(imagePaths, p)
	}
	b := issuereport.Bundle{
		Meta: issuereport.Meta{
			ID:        "report-with-images",
			Note:      "screenshots attached",
			CreatedAt: time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC),
		},
		Dir:        dir,
		ImagePaths: imagePaths,
	}

	var gotImages [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			t.Errorf("server: parse multipart: %v", err)
			return
		}
		for _, fh := range r.MultipartForm.File["image"] {
			f, err := fh.Open()
			if err != nil {
				t.Errorf("server: open image part: %v", err)
				continue
			}
			data := make([]byte, fh.Size)
			_, _ = f.Read(data)
			f.Close()
			gotImages = append(gotImages, data)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := uploadIssueReport(context.Background(), registeredCfg(srv.URL), b); err != nil {
		t.Fatalf("uploadIssueReport: %v", err)
	}
	if len(gotImages) != len(imageContents) {
		t.Fatalf("got %d image parts, want %d", len(gotImages), len(imageContents))
	}
	for i, want := range imageContents {
		if string(gotImages[i]) != want {
			t.Errorf("image part %d = %q, want %q", i, gotImages[i], want)
		}
	}
}

// A bundle that never captured a locale (older bundle on disk from before
// ut-docs#397, or the JS-less path) must still SEND the locale field — as an
// empty string, which the cloud side reads as "no locale known, auto-detect".
// Skipping the field entirely is not the same contract, so the assertion is
// on the part's presence, not just FormValue (which can't tell empty from
// absent).
func TestUploadIssueReportSendsEmptyLocaleWhenUnknown(t *testing.T) {
	withTempPendingDir(t)
	if _, err := issuereport.Save("no locale captured", "", []byte("fake-audio"), nil, nil); err != nil {
		t.Fatalf("Save: %v", err)
	}
	bundles, err := issuereport.Pending()
	if err != nil || len(bundles) != 1 {
		t.Fatalf("Pending: %v (%d)", err, len(bundles))
	}
	if bundles[0].Meta.Locale != "" {
		t.Fatalf("Meta.Locale = %q, want empty", bundles[0].Meta.Locale)
	}

	var gotLocaleValues []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(10 << 20)
		gotLocaleValues = append([]string(nil), r.MultipartForm.Value["locale"]...)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := uploadIssueReport(context.Background(), registeredCfg(srv.URL), bundles[0]); err != nil {
		t.Fatalf("uploadIssueReport: %v", err)
	}
	if len(gotLocaleValues) != 1 || gotLocaleValues[0] != "" {
		t.Fatalf("locale field values = %q, want exactly one empty string", gotLocaleValues)
	}
}

// A bundle with no video must omit the video part entirely rather than send
// an empty one — attachFile's "only if VideoPath != ”" guard.
func TestUploadIssueReportOmitsVideoWhenAbsent(t *testing.T) {
	withTempPendingDir(t)
	if _, err := issuereport.Save("no video", "", []byte("fake-audio"), nil, nil); err != nil {
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

// A note-only bundle (no voice recording) must omit the audio part entirely
// rather than send an empty one — mirrors the existing video-absent case.
func TestUploadIssueReportOmitsAudioWhenAbsent(t *testing.T) {
	withTempPendingDir(t)
	if _, err := issuereport.Save("typed only, no voice note", "", nil, nil, nil); err != nil {
		t.Fatalf("Save: %v", err)
	}
	bundles, err := issuereport.Pending()
	if err != nil || len(bundles) != 1 {
		t.Fatalf("Pending: %v (%d)", err, len(bundles))
	}
	if bundles[0].AudioPath != "" {
		t.Fatalf("AudioPath = %q, want empty", bundles[0].AudioPath)
	}

	var hadAudioPart bool
	var gotNote string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(10 << 20)
		hadAudioPart = len(r.MultipartForm.File["audio"]) > 0
		gotNote = r.FormValue("note")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := uploadIssueReport(context.Background(), registeredCfg(srv.URL), bundles[0]); err != nil {
		t.Fatalf("uploadIssueReport: %v", err)
	}
	if hadAudioPart {
		t.Fatal("audio part sent for a bundle with no voice recording")
	}
	if gotNote != "typed only, no voice note" {
		t.Fatalf("note = %q", gotNote)
	}
}

func TestUploadIssueReportUnregistered(t *testing.T) {
	withTempPendingDir(t)
	if _, err := issuereport.Save("note", "", []byte("a"), nil, nil); err != nil {
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
	if _, err := issuereport.Save("note", "", []byte("a"), nil, nil); err != nil {
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
	if _, err := issuereport.Save("note", "", []byte("a"), nil, nil); err != nil {
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
	okID, err := issuereport.Save("this one uploads", "", []byte("audio-ok"), nil, nil)
	if err != nil {
		t.Fatalf("Save ok bundle: %v", err)
	}
	failID, err := issuereport.Save("this one fails", "", []byte("audio-fail"), nil, nil)
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
	if _, err := os.Stat(filepath.Join(issuereport.PendingDir, okID)); !os.IsNotExist(err) {
		t.Fatalf("discarded bundle dir for %q should be gone, stat err = %v", okID, err)
	}
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
	if _, err := issuereport.Save("note", "", []byte("hello-bytes"), nil, nil); err != nil {
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
