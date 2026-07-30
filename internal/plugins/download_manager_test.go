package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestDownloadValidation(t *testing.T) {
	dm := NewDownloadManager(t.TempDir())
	ctx := context.Background()
	if _, err := dm.Download(ctx, &DownloadRequest{}); err == nil || !strings.Contains(err.Error(), "URL is required") {
		t.Fatalf("empty url: %v", err)
	}
	if _, err := dm.Download(ctx, &DownloadRequest{URL: "http://x"}); err == nil || !strings.Contains(err.Error(), "plugin ID is required") {
		t.Fatalf("empty id: %v", err)
	}
	if _, err := dm.Download(ctx, &DownloadRequest{URL: "http://x", PluginID: "p"}); err == nil || !strings.Contains(err.Error(), "checksum is required") {
		t.Fatalf("empty checksum: %v", err)
	}
}

func TestDownloadHappyPath(t *testing.T) {
	content := []byte("plugin bundle bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(content)
	}))
	defer srv.Close()

	dm := NewDownloadManager(t.TempDir())
	res, err := dm.Download(context.Background(), &DownloadRequest{
		URL: srv.URL, PluginID: "com.test.happy", ExpectedChecksum: sha256Hex(content),
	})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if !res.Verified || res.BytesDownloaded != int64(len(content)) || res.ResumedFrom != 0 {
		t.Fatalf("result: %+v", res)
	}
	got, err := os.ReadFile(res.FilePath)
	if err != nil || string(got) != string(content) {
		t.Fatalf("part file content mismatch: %v", err)
	}
}

func TestDownloadResumesFromPartFile(t *testing.T) {
	content := []byte("0123456789abcdef")
	var sawRange atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rng := r.Header.Get("Range")
		sawRange.Store(rng)
		if strings.HasPrefix(rng, "bytes=") {
			from, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rng, "bytes="), "-"))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(content[from:])
			return
		}
		_, _ = w.Write(content)
	}))
	defer srv.Close()

	tmp := t.TempDir()
	// Pre-seed half the file as a .part.
	if err := os.WriteFile(filepath.Join(tmp, "com.test.resume.part"), content[:8], 0o644); err != nil {
		t.Fatalf("seed part: %v", err)
	}

	dm := NewDownloadManager(tmp)
	res, err := dm.Download(context.Background(), &DownloadRequest{
		URL: srv.URL, PluginID: "com.test.resume", ExpectedChecksum: sha256Hex(content),
	})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if res.ResumedFrom != 8 {
		t.Fatalf("ResumedFrom = %d, want 8", res.ResumedFrom)
	}
	if got, _ := sawRange.Load().(string); got != "bytes=8-" {
		t.Fatalf("Range header = %q", got)
	}
	got, err := os.ReadFile(res.FilePath)
	if err != nil || string(got) != string(content) {
		t.Fatalf("resumed content mismatch: %q, %v", got, err)
	}
}

func TestDownloadRestartsWhenServerIgnoresRange(t *testing.T) {
	content := []byte("full content again")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always 200 + full body, even for Range requests.
		_, _ = w.Write(content)
	}))
	defer srv.Close()

	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "com.test.norange.part"), []byte("stale-half"), 0o644); err != nil {
		t.Fatalf("seed part: %v", err)
	}

	dm := NewDownloadManager(tmp)
	res, err := dm.Download(context.Background(), &DownloadRequest{
		URL: srv.URL, PluginID: "com.test.norange", ExpectedChecksum: sha256Hex(content),
	})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	// The stale partial must have been discarded, not prepended.
	if got, _ := os.ReadFile(res.FilePath); string(got) != string(content) {
		t.Fatalf("content after restart: %q", got)
	}
}

func TestDownloadNonOKStatusRetriesThenFails(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	dm := NewDownloadManager(t.TempDir())
	dm.maxRetries = 1
	dm.retryBackoff = time.Millisecond
	_, err := dm.Download(context.Background(), &DownloadRequest{
		URL: srv.URL, PluginID: "com.test.404", ExpectedChecksum: strings.Repeat("0", 64),
	})
	if err == nil || !strings.Contains(err.Error(), "unexpected status code: 404") {
		t.Fatalf("404: %v", err)
	}
	if hits.Load() != 2 { // transient-class errors ARE retried
		t.Fatalf("hits = %d, want 2", hits.Load())
	}
}

func TestDownloadCancelledContextStopsRetries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dm := NewDownloadManager(t.TempDir())
	dm.retryBackoff = time.Millisecond
	_, err := dm.Download(ctx, &DownloadRequest{
		URL: "http://127.0.0.1:1/x", PluginID: "com.test.cancel", ExpectedChecksum: strings.Repeat("0", 64),
	})
	if err == nil {
		t.Fatalf("cancelled download succeeded")
	}
}

func TestPromoteToPermanentRename(t *testing.T) {
	dm := NewDownloadManager(t.TempDir())
	src := filepath.Join(t.TempDir(), "bundle.part")
	if err := os.WriteFile(src, []byte("data"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	destDir := filepath.Join(t.TempDir(), "downloads")
	if err := dm.PromoteToPermanent(src, destDir, "bundle.tar.gz"); err != nil {
		t.Fatalf("PromoteToPermanent: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(destDir, "bundle.tar.gz")); err != nil || string(got) != "data" {
		t.Fatalf("promoted content: %q, %v", got, err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf(".part file still present after promote")
	}

	// Failure: destination unwritable (dest name is an existing directory —
	// rename AND the copy fallback both fail).
	src2 := filepath.Join(t.TempDir(), "b2.part")
	if err := os.WriteFile(src2, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(destDir, "taken"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := dm.PromoteToPermanent(src2, destDir, "taken"); err == nil {
		t.Fatalf("promote onto a directory succeeded")
	}
}

func TestCopyFile(t *testing.T) {
	dm := NewDownloadManager(t.TempDir())
	src := filepath.Join(t.TempDir(), "src.bin")
	if err := os.WriteFile(src, []byte("copy me"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "dst.bin")
	if err := dm.copyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	if got, _ := os.ReadFile(dst); string(got) != "copy me" {
		t.Fatalf("copied content: %q", got)
	}
	if err := dm.copyFile(filepath.Join(t.TempDir(), "missing"), dst); err == nil {
		t.Fatalf("copy of missing source succeeded")
	}
}

func TestCleanupPartFile(t *testing.T) {
	tmp := t.TempDir()
	dm := NewDownloadManager(tmp)
	// Missing part file is a no-op.
	if err := dm.CleanupPartFile("com.test.nothing"); err != nil {
		t.Fatalf("cleanup missing: %v", err)
	}
	part := filepath.Join(tmp, "com.test.clean.part")
	if err := os.WriteFile(part, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := dm.CleanupPartFile("com.test.clean"); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Fatalf("part file survived cleanup")
	}
}

func TestIsRetryable(t *testing.T) {
	dm := NewDownloadManager(t.TempDir())
	cases := []struct {
		err  error
		want bool
	}{
		{context.Canceled, false},
		{context.DeadlineExceeded, false},
		{fmt.Errorf("wrapped: %w", context.Canceled), false},
		{fmt.Errorf("%w: expected a, got b", errChecksumMismatch), false},
		{fmt.Errorf("unexpected status code: 500"), true},
		{fmt.Errorf("HTTP request failed: connection refused"), true},
	}
	for _, c := range cases {
		if got := dm.isRetryable(c.err); got != c.want {
			t.Errorf("isRetryable(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}
