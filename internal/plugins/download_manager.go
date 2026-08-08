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
	"time"

	"github.com/universaltill/universal-till/internal/logging"
)

// DownloadManager handles resumable plugin artifact downloads with checksum verification
type DownloadManager struct {
	tmpDir         string
	httpClient     *http.Client
	maxRetries     int
	retryBackoff   time.Duration
	downloadBudget int64 // max bytes per download
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
		tmpDir: tmpDir,
		httpClient: &http.Client{
			Timeout: 30 * time.Minute, // long timeout for large plugins
		},
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
	partFile := filepath.Join(dm.tmpDir, fmt.Sprintf("%s.part", req.PluginID))

	// Check if partial download exists
	var resumeFrom int64
	if fi, err := os.Stat(partFile); err == nil {
		resumeFrom = fi.Size()
		log.Infof("[Download] Resuming download for %s from byte %d", req.PluginID, resumeFrom)
	}

	// Attempt download with retries
	var lastErr error
	for attempt := 0; attempt <= dm.maxRetries; attempt++ {
		if attempt > 0 {
			backoff := dm.retryBackoff * time.Duration(attempt)
			log.Warnf("[Download] Retry %d/%d for %s after %v", attempt, dm.maxRetries, req.PluginID, backoff)

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}

		// Try download
		result, err := dm.downloadOnce(ctx, req, partFile, resumeFrom, maxSize)
		if err == nil {
			return result, nil
		}

		lastErr = err
		log.Warnf("[Download] Attempt %d failed for %s: %v", attempt+1, req.PluginID, err)

		// Check if we should retry
		if !dm.isRetryable(err) {
			break
		}
	}

	return nil, fmt.Errorf("download failed after %d retries: %w", dm.maxRetries+1, lastErr)
}

// downloadOnce performs a single download attempt with resume support
func (dm *DownloadManager) downloadOnce(ctx context.Context, req *DownloadRequest, partFile string, resumeFrom, maxSize int64) (*DownloadResult, error) {
	log := logging.L()

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, req.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Add Range header if resuming
	if resumeFrom > 0 {
		httpReq.Header.Set("Range", fmt.Sprintf("bytes=%d-", resumeFrom))
	}

	// Execute request
	resp, err := dm.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	// Check status
	if resumeFrom > 0 && resp.StatusCode != http.StatusPartialContent {
		// Server doesn't support resume, start over
		log.Warnf("[Download] Server doesn't support resume for %s, starting from beginning", req.PluginID)
		resumeFrom = 0
		os.Remove(partFile) // delete partial file

		// Retry without Range header
		httpReq.Header.Del("Range")
		resp.Body.Close()

		resp, err = dm.httpClient.Do(httpReq)
		if err != nil {
			return nil, fmt.Errorf("HTTP request failed: %w", err)
		}
		defer resp.Body.Close()
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	// Open/create .part file. O_RDWR, not O_WRONLY: the resume path below
	// re-reads the existing content to seed the hash, and reading a
	// write-only descriptor fails with EBADF — which made EVERY resumed
	// download fail (proven by TestDownloadResumesFromPartFile).
	var f *os.File
	if resumeFrom > 0 {
		f, err = os.OpenFile(partFile, os.O_RDWR|os.O_APPEND, 0644)
	} else {
		f, err = os.Create(partFile)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to open part file: %w", err)
	}
	defer f.Close()

	// Download with size limit
	hasher := sha256.New()

	// If resuming, we need to rehash the existing content
	if resumeFrom > 0 {
		f.Seek(0, io.SeekStart)
		if _, err := io.Copy(hasher, f); err != nil {
			return nil, fmt.Errorf("failed to hash existing content: %w", err)
		}
		f.Seek(0, io.SeekEnd)
	}

	limitedReader := io.LimitReader(resp.Body, maxSize-resumeFrom)
	multiWriter := io.MultiWriter(f, hasher)

	bytesDownloaded, err := io.Copy(multiWriter, limitedReader)
	if err != nil {
		return nil, fmt.Errorf("download interrupted: %w", err)
	}

	totalBytes := resumeFrom + bytesDownloaded
	log.Infof("[Download] Downloaded %d bytes for %s (total: %d)", bytesDownloaded, req.PluginID, totalBytes)

	// Verify checksum
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
	partFile := filepath.Join(dm.tmpDir, fmt.Sprintf("%s.part", pluginID))
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
