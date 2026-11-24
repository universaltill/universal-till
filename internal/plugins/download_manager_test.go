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
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, len(content)-1, len(content)))
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

// ut-docs#2891 review M2 sweep: the .part file name is built from the
// listing id a manager's HTTP request supplies. It must stay inside tmpDir.
func TestDownloadPartFileStaysInTmpDir(t *testing.T) {
	content := []byte("bundle")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(content)
	}))
	defer srv.Close()
	root := t.TempDir()
	tmp := filepath.Join(root, "a", "b", "tmp")
	dm := NewDownloadManager(tmp)
	res, err := dm.Download(context.Background(), &DownloadRequest{
		URL: srv.URL, PluginID: "../../escaped", ExpectedChecksum: sha256Hex(content),
	})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if filepath.Dir(res.FilePath) != filepath.Clean(tmp) {
		t.Fatalf("part file %s escaped tmp dir %s", res.FilePath, tmp)
	}
	if _, err := os.Stat(filepath.Join(root, "a", "escaped.part")); err == nil {
		t.Fatal("a traversal listing id wrote outside the tmp dir")
	}
	// CleanupPartFile must resolve the same (sanitized) name.
	if err := dm.CleanupPartFile("../../escaped"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(res.FilePath); !os.IsNotExist(err) {
		t.Fatal("CleanupPartFile did not remove the sanitized part file")
	}
}

// ut-docs#6975: a server that answers a Range request with 206 but a
// Content-Range that does not start at the requested byte must never have its
// body appended: the partial is dropped and the file re-fetched from zero.
func TestDownloadWrongContentRangeRestartsFromZero(t *testing.T) {
	content := []byte("0123456789abcdefghij")
	var ranged, plain atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			ranged.Add(1)
			w.Header().Set("Content-Range", "bytes 0-19/20") // wrong start
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(content)
			return
		}
		plain.Add(1)
		_, _ = w.Write(content)
	}))
	defer srv.Close()

	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "com.test.badrange.part"), content[:8], 0o644); err != nil {
		t.Fatal(err)
	}
	dm := NewDownloadManager(tmp)
	dm.retryBackoff = time.Millisecond
	res, err := dm.Download(context.Background(), &DownloadRequest{
		URL: srv.URL, PluginID: "com.test.badrange", ExpectedChecksum: sha256Hex(content),
	})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if got, _ := os.ReadFile(res.FilePath); string(got) != string(content) {
		t.Fatalf("content = %q", got)
	}
	if ranged.Load() != 1 || plain.Load() != 1 {
		t.Fatalf("ranged=%d plain=%d, want 1 and 1", ranged.Load(), plain.Load())
	}
	// The bad range must be caught by the Content-Range check itself, not
	// rescued later by the checksum-mismatch scratch retry.
	if n := dm.scratchRetries.Load(); n != 0 {
		t.Fatalf("scratch retries = %d, want 0 (range check must reject first)", n)
	}
}

// ut-docs#6975 log evidence: a 200 full body to a Range request must not be
// appended to the partial (1674609 bytes "resumed" from 97066).
func TestDownloadIgnoredRangeDoesNotAppendFullBody(t *testing.T) {
	content := []byte(strings.Repeat("plugin-bytes-", 50))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(content)
	}))
	defer srv.Close()
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "com.test.full.part"), content[:97%len(content)+30], 0o644); err != nil {
		t.Fatal(err)
	}
	dm := NewDownloadManager(tmp)
	res, err := dm.Download(context.Background(), &DownloadRequest{
		URL: srv.URL, PluginID: "com.test.full", ExpectedChecksum: sha256Hex(content),
	})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if n := dm.scratchRetries.Load(); n != 0 {
		t.Fatalf("scratch retries = %d, want 0 (200 body must be used from zero, not appended)", n)
	}
	if res.BytesDownloaded != int64(len(content)) || res.ResumedFrom != 0 {
		t.Fatalf("result %+v", res)
	}
	fi, _ := os.Stat(res.FilePath)
	if fi.Size() != int64(len(content)) {
		t.Fatalf("file size %d, want %d", fi.Size(), len(content))
	}
}

// A resumed download whose checksum then mismatches (bad splice) is retried
// ONCE from scratch with no Range header; a from-zero mismatch is permanent.
func TestDownloadChecksumMismatchAfterResumeRetriesOnceFromScratch(t *testing.T) {
	content := []byte("0123456789abcdefghij")
	var rangedReqs, plainReqs atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rng := r.Header.Get("Range"); rng != "" {
			rangedReqs.Add(1)
			w.Header().Set("Content-Range", "bytes 8-19/20")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("XXXXXXXXXXXX")) // right range, corrupt bytes
			return
		}
		plainReqs.Add(1)
		_, _ = w.Write(content)
	}))
	defer srv.Close()
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "com.test.splice.part"), content[:8], 0o644); err != nil {
		t.Fatal(err)
	}
	dm := NewDownloadManager(tmp)
	dm.retryBackoff = time.Millisecond
	res, err := dm.Download(context.Background(), &DownloadRequest{
		URL: srv.URL, PluginID: "com.test.splice", ExpectedChecksum: sha256Hex(content),
	})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if got, _ := os.ReadFile(res.FilePath); string(got) != string(content) {
		t.Fatalf("content = %q", got)
	}
	if rangedReqs.Load() != 1 || plainReqs.Load() != 1 {
		t.Fatalf("ranged=%d plain=%d", rangedReqs.Load(), plainReqs.Load())
	}
}

func TestDownloadChecksumMismatchFromScratchIsPermanent(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("wrong bytes"))
	}))
	defer srv.Close()
	tmp := t.TempDir()
	dm := NewDownloadManager(tmp)
	dm.retryBackoff = time.Millisecond
	_, err := dm.Download(context.Background(), &DownloadRequest{
		URL: srv.URL, PluginID: "com.test.perm", ExpectedChecksum: strings.Repeat("0", 64),
	})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d, want 1", hits.Load())
	}
	if _, statErr := os.Stat(filepath.Join(tmp, "com.test.perm.part")); !os.IsNotExist(statErr) {
		t.Fatalf("corrupt part file left behind")
	}
}

// The resume offset must be re-read each attempt: attempt 1 appends part of
// the body then drops; attempt 2 has to continue from the NEW size.
func TestDownloadRetryResumesFromCurrentPartSize(t *testing.T) {
	content := []byte(strings.Repeat("0123456789", 20))
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		from := 0
		if rng := r.Header.Get("Range"); rng != "" {
			from, _ = strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rng, "bytes="), "-"))
			w.Header().Set("Content-Range", "bytes "+strconv.Itoa(from)+"-"+strconv.Itoa(len(content)-1)+"/"+strconv.Itoa(len(content)))
			w.WriteHeader(http.StatusPartialContent)
		}
		if n == 1 { // announce the full length, send 60 bytes, hang up
			w.Header().Set("Content-Length", strconv.Itoa(len(content)-from))
			_, _ = w.Write(content[from : from+60])
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			panic(http.ErrAbortHandler)
		}
		_, _ = w.Write(content[from:])
	}))
	defer srv.Close()
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "com.test.stale.part"), content[:10], 0o644); err != nil {
		t.Fatal(err)
	}
	dm := NewDownloadManager(tmp)
	dm.retryBackoff = time.Millisecond
	res, err := dm.Download(context.Background(), &DownloadRequest{
		URL: srv.URL, PluginID: "com.test.stale", ExpectedChecksum: sha256Hex(content),
	})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if res.ResumedFrom != 70 {
		t.Fatalf("ResumedFrom = %d, want 70", res.ResumedFrom)
	}
}
