package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/netaccess"
)

// DownloadManager handles resumable plugin artifact downloads with checksum verification
type DownloadManager struct {
	tmpDir         string
	httpClient     *http.Client
	maxRetries     int
	retryBackoff   time.Duration
	downloadBudget int64        // max bytes per download
	scratchRetries atomic.Int32 // checksum-mismatch-after-resume restarts (tests assert it)
}

// DownloadRequest contains all information needed to download a plugin artifact
type DownloadRequest struct {
	URL              string // artifact download URL (with token)
	PluginID         string // unique plugin identifier
	ExpectedChecksum string // SHA256 hash to verify against
	MaxSizeBytes     int64  // max allowed artifact size
}

// DownloadResult contains the outcome of a download operation
type DownloadResult struct {
	FilePath        string
	ActualChecksum  string
	BytesDownloaded int64
	ResumedFrom     int64
	Verified        bool
}

// NewDownloadManager creates a new download manager
func NewDownloadManager(tmpDir string) *DownloadManager {
	return &DownloadManager{
		tmpDir:         tmpDir,
		httpClient:     netaccess.NewClient(30 * time.Minute), // long timeout for large plugins
		maxRetries:     5,
		retryBackoff:   2 * time.Second,
		downloadBudget: 500 * 1024 * 1024, // 500 MB default
	}
}

// Download downloads a plugin artifact with resume support and checksum verification
func (dm *DownloadManager) Download(ctx context.Context, req *DownloadRequest) (*DownloadResult, error) {
	log := logging.L()

	// Validate request
	if req.URL == "" {
		return nil, fmt.Errorf("download URL is required")
	}
	if req.PluginID == "" {
		return nil, fmt.Errorf("plugin ID is required")
	}
	if req.ExpectedChecksum == "" {
		return nil, fmt.Errorf("expected checksum is required")
	}

	// Determine max size
	maxSize := req.MaxSizeBytes
	if maxSize <= 0 {
		maxSize = dm.downloadBudget
	}

	// Create .part file path. MkdirAll first: the tmp dir does not exist on a
	// fresh till until something else creates it, and the LAN plugin sync
	// (ut-docs#460) can be the very first download a replica ever makes — no
	// manager has necessarily opened the plugins page there.
	if err := os.MkdirAll(dm.tmpDir, 0o755); err != nil {
		return nil, fmt.Errorf("create download tmp dir: %w", err)
	}
	partFile := dm.partFilePath(req.PluginID)

	// Attempt download with retries. The resume offset is re-read from the
	// .part file on EVERY attempt: a failed attempt may have appended more
	// bytes, and a stale offset made the next Range request (and the hash
	// seed) disagree with the file (ut-docs#6975).
	var lastErr error
	scratchRetryUsed := false
	forceScratch := false
	for attempt := 0; attempt <= dm.maxRetries; attempt++ {
		if attempt > 0 && !forceScratch {
			backoff := dm.retryBackoff * time.Duration(attempt)
			log.Warnf("[Download] Retry %d/%d for %s after %v", attempt, dm.maxRetries, req.PluginID, backoff)

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}

		var resumeFrom int64
		if forceScratch {
			_ = os.Remove(partFile)
			forceScratch = false
		} else if fi, err := os.Stat(partFile); err == nil {
			resumeFrom = fi.Size()
			if resumeFrom > 0 {
				log.Infof("[Download] Resuming download for %s from byte %d", req.PluginID, resumeFrom)
			}
		}

		result, err := dm.downloadOnce(ctx, req, partFile, resumeFrom, maxSize)
		if err == nil {
			return result, nil
		}

		lastErr = err
		log.Warnf("[Download] Attempt %d failed for %s: %v", attempt+1, req.PluginID, err)

		// A server that ignored/garbled Range: the part file is already
		// removed; start again from byte zero straight away.
		if errors.Is(err, errRangeRejected) {
			forceScratch = true
			continue
		}
		// A checksum mismatch after a RESUMED download may just be a bad
		// splice: drop the partial file and retry once from scratch, no Range.
		// A mismatch on a from-zero download is permanent.
		if errors.Is(err, errChecksumMismatch) {
			_ = os.Remove(partFile)
			if resumeFrom > 0 && !scratchRetryUsed {
				scratchRetryUsed = true
				dm.scratchRetries.Add(1)
				forceScratch = true
				log.Warnf("[Download] Checksum mismatch after resume for %s: retrying once from scratch", req.PluginID)
				continue
			}
			break
		}

		if !dm.isRetryable(err) {
			break
		}
	}

	return nil, fmt.Errorf("download failed after %d retries: %w", dm.maxRetries+1, lastErr)
}

// errRangeRejected: the server answered a Range request with something other
// than the exact continuation requested (wrong Content-Range, 416). The part
// file has been deleted; the caller restarts from zero.
var errRangeRejected = errors.New("server did not honour resume range")

// parseContentRangeStart returns the first byte of a "bytes a-b/c" header.
func parseContentRangeStart(h string) (int64, bool) {
	h = strings.TrimSpace(h)
	if !strings.HasPrefix(h, "bytes ") {
		return 0, false
	}
	rest := strings.TrimPrefix(h, "bytes ")
	dash := strings.IndexByte(rest, '-')
	if dash <= 0 {
		return 0, false
	}
	start, err := strconv.ParseInt(strings.TrimSpace(rest[:dash]), 10, 64)
	if err != nil || start < 0 {
		return 0, false
	}
	return start, true
}

// downloadOnce performs a single download attempt with resume support
func (dm *DownloadManager) downloadOnce(ctx context.Context, req *DownloadRequest, partFile string, resumeFrom, maxSize int64) (*DownloadResult, error) {
	log := logging.L()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, req.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	if resumeFrom > 0 {
		httpReq.Header.Set("Range", fmt.Sprintf("bytes=%d-", resumeFrom))
	}

	resp, err := dm.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resumeFrom > 0 {
		switch {
		case resp.StatusCode == http.StatusOK:
			// Server ignored Range and is sending the whole body: appending
			// it to the partial file corrupts it. Truncate and use this body
			// from byte zero.
			log.Warnf("[Download] Server ignored Range for %s, restarting from beginning", req.PluginID)
			resumeFrom = 0
		case resp.StatusCode == http.StatusPartialContent:
			start, ok := parseContentRangeStart(resp.Header.Get("Content-Range"))
			if !ok || start != resumeFrom {
				_ = os.Remove(partFile)
				return nil, fmt.Errorf("%w: wanted start %d, got Content-Range %q", errRangeRejected, resumeFrom, resp.Header.Get("Content-Range"))
			}
		case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
			_ = os.Remove(partFile)
			return nil, fmt.Errorf("%w: status 416", errRangeRejected)
		default:
			return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
		}
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}
	if resumeFrom == 0 && resp.StatusCode == http.StatusPartialContent {
		return nil, fmt.Errorf("unexpected status code: %d without a Range request", resp.StatusCode)
	}

	// O_RDWR, not O_WRONLY: the resume path re-reads the existing content to
	// seed the hash (EBADF otherwise; TestDownloadResumesFromPartFile).
	var f *os.File
	if resumeFrom > 0 {
		f, err = os.OpenFile(partFile, os.O_RDWR|os.O_APPEND, 0644)
	} else {
		f, err = os.Create(partFile) // truncates
	}
	if err != nil {
		return nil, fmt.Errorf("failed to open part file: %w", err)
	}
	defer f.Close()

	hasher := sha256.New()

	// If resuming, rehash the existing content
	if resumeFrom > 0 {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return nil, fmt.Errorf("failed to seek part file: %w", err)
		}
		if _, err := io.CopyN(hasher, f, resumeFrom); err != nil {
			return nil, fmt.Errorf("failed to hash existing content: %w", err)
		}
	}

	limitedReader := io.LimitReader(resp.Body, maxSize-resumeFrom)
	multiWriter := io.MultiWriter(f, hasher)

	bytesDownloaded, err := io.Copy(multiWriter, limitedReader)
	if err != nil {
		return nil, fmt.Errorf("download interrupted: %w", err)
	}

	totalBytes := resumeFrom + bytesDownloaded
	log.Infof("[Download] Downloaded %d bytes for %s (total: %d)", bytesDownloaded, req.PluginID, totalBytes)

	actualChecksum := hex.EncodeToString(hasher.Sum(nil))
	if actualChecksum != req.ExpectedChecksum {
		return nil, fmt.Errorf("%w: expected %s, got %s", errChecksumMismatch, req.ExpectedChecksum, actualChecksum)
	}

	return &DownloadResult{
		FilePath:        partFile,
		ActualChecksum:  actualChecksum,
		BytesDownloaded: totalBytes,
		ResumedFrom:     resumeFrom,
		Verified:        true,
	}, nil
}

// PromoteToPermanent moves a .part file to its final destination
func (dm *DownloadManager) PromoteToPermanent(partFile, destDir, destName string) error {
	log := logging.L()

	// Ensure destination directory exists
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}

	destPath := filepath.Join(destDir, destName)

	// Move file
	if err := os.Rename(partFile, destPath); err != nil {
		// If rename fails (cross-device), try copy + delete
		if err := dm.copyFile(partFile, destPath); err != nil {
			return fmt.Errorf("failed to promote file: %w", err)
		}
		os.Remove(partFile)
	}

	log.Infof("[Download] Promoted %s to %s", partFile, destPath)
	return nil
}

// CleanupPartFile removes a .part file (used on permanent failure)
func (dm *DownloadManager) CleanupPartFile(pluginID string) error {
	partFile := dm.partFilePath(pluginID)
	if err := os.Remove(partFile); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// copyFile copies a file from src to dst
func (dm *DownloadManager) copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return err
	}

	return dstFile.Sync()
}

// errChecksumMismatch marks a completed download whose content hash doesn't
// match the marketplace-issued checksum. Permanent: re-downloading the same
// artifact (worse — RESUMING from the already-complete corrupt .part file)
// can never fix it.
var errChecksumMismatch = errors.New("checksum mismatch")

// isRetryable determines if an error should trigger a retry
func (dm *DownloadManager) isRetryable(err error) bool {
	// Retry on network errors, timeouts, and transient HTTP errors.
	// Don't retry on context cancellation or checksum mismatches. Errors from
	// downloadOnce arrive wrapped, so plain == comparison would never match.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, errChecksumMismatch) {
		return false
	}
	return true
}

// partFilePath is the resume file for one download. The id is the listing id
// a manager's request supplies, so it is flattened to one path segment —
// "../../x" must never write outside tmpDir (ut-docs#2891 review M2 sweep).
func (dm *DownloadManager) partFilePath(pluginID string) string {
	return filepath.Join(dm.tmpDir, sanitizePathSegment(pluginID)+".part")
}
