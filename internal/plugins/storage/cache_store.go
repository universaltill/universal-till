package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// CacheStore manages plugin download caching with .part files and disk quotas
type CacheStore struct {
	cacheDir   string
	quotaBytes int64
	mu         sync.RWMutex
}

// NewCacheStore creates a cache store with a disk quota
func NewCacheStore(cacheDir string, quotaBytes int64) (*CacheStore, error) {
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create cache dir: %w", err)
	}
	return &CacheStore{
		cacheDir:   cacheDir,
		quotaBytes: quotaBytes,
	}, nil
}

// StartDownload creates a .part file for resumable downloads
func (cs *CacheStore) StartDownload(pluginID, version string) (string, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	partPath := cs.partPath(pluginID, version)

	// Check quota before starting
	if err := cs.checkQuota(); err != nil {
		return "", err
	}

	// Create parent directory
	if err := os.MkdirAll(filepath.Dir(partPath), 0755); err != nil {
		return "", fmt.Errorf("failed to create cache subdirectory: %w", err)
	}

	return partPath, nil
}

// PromoteDownload moves a completed .part file to final artifact location
func (cs *CacheStore) PromoteDownload(pluginID, version string, checksum []byte) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	partPath := cs.partPath(pluginID, version)
	finalPath := cs.artifactPath(pluginID, version)

	// Verify checksum
	if err := cs.verifyChecksum(partPath, checksum); err != nil {
		return fmt.Errorf("checksum verification failed: %w", err)
	}

	// Rename .part to final artifact
	if err := os.Rename(partPath, finalPath); err != nil {
		return fmt.Errorf("failed to promote artifact: %w", err)
	}

	return nil
}

// GetArtifact returns the path to a cached artifact if it exists
func (cs *CacheStore) GetArtifact(pluginID, version string) (string, bool) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	path := cs.artifactPath(pluginID, version)
	if _, err := os.Stat(path); err == nil {
		return path, true
	}
	return "", false
}

// GetPartialDownload returns the path and current size of a .part file
func (cs *CacheStore) GetPartialDownload(pluginID, version string) (string, int64, bool) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	path := cs.partPath(pluginID, version)
	info, err := os.Stat(path)
	if err != nil {
		return "", 0, false
	}
	return path, info.Size(), true
}

// CancelDownload removes a .part file
func (cs *CacheStore) CancelDownload(pluginID, version string) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	partPath := cs.partPath(pluginID, version)
	if err := os.Remove(partPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to cancel download: %w", err)
	}
	return nil
}

// DeleteArtifact removes a cached artifact
func (cs *CacheStore) DeleteArtifact(pluginID, version string) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	artifactPath := cs.artifactPath(pluginID, version)
	if err := os.Remove(artifactPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete artifact: %w", err)
	}
	return nil
}

// GetCacheSize returns the total size of all cached files
func (cs *CacheStore) GetCacheSize() (int64, error) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	var totalSize int64
	err := filepath.Walk(cs.cacheDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			totalSize += info.Size()
		}
		return nil
	})
	return totalSize, err
}

// CleanOldest removes oldest artifacts until cache is under quota
func (cs *CacheStore) CleanOldest() error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	currentSize, err := cs.getCacheSizeUnlocked()
	if err != nil {
		return err
	}

	if currentSize <= cs.quotaBytes {
		return nil // Already under quota
	}

	// Collect all artifacts with modification times
	type artifact struct {
		path    string
		size    int64
		modTime int64
	}
	var artifacts []artifact

	err = filepath.Walk(cs.cacheDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && filepath.Ext(path) != ".part" {
			artifacts = append(artifacts, artifact{
				path:    path,
				size:    info.Size(),
				modTime: info.ModTime().Unix(),
			})
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Sort by modification time (oldest first)
	for i := 0; i < len(artifacts)-1; i++ {
		for j := i + 1; j < len(artifacts); j++ {
			if artifacts[i].modTime > artifacts[j].modTime {
				artifacts[i], artifacts[j] = artifacts[j], artifacts[i]
			}
		}
	}

	// Remove oldest until under quota
	for _, a := range artifacts {
		if currentSize <= cs.quotaBytes {
			break
		}
		if err := os.Remove(a.path); err != nil {
			return fmt.Errorf("failed to remove old artifact: %w", err)
		}
		currentSize -= a.size
	}

	return nil
}

// partPath returns the .part file path for a download in progress
func (cs *CacheStore) partPath(pluginID, version string) string {
	filename := fmt.Sprintf("%s-%s.utplugin.part", pluginID, version)
	return filepath.Join(cs.cacheDir, filename)
}

// artifactPath returns the final artifact path
func (cs *CacheStore) artifactPath(pluginID, version string) string {
	filename := fmt.Sprintf("%s-%s.utplugin", pluginID, version)
	return filepath.Join(cs.cacheDir, filename)
}

// verifyChecksum validates SHA256 checksum of a file
func (cs *CacheStore) verifyChecksum(path string, expectedChecksum []byte) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}

	actualChecksum := h.Sum(nil)
	if hex.EncodeToString(actualChecksum) != hex.EncodeToString(expectedChecksum) {
		return fmt.Errorf("checksum mismatch: expected %x, got %x", expectedChecksum, actualChecksum)
	}

	return nil
}

// checkQuota ensures there's space available
func (cs *CacheStore) checkQuota() error {
	currentSize, err := cs.getCacheSizeUnlocked()
	if err != nil {
		return err
	}

	if currentSize >= cs.quotaBytes {
		return fmt.Errorf("cache quota exceeded: %d bytes used of %d quota", currentSize, cs.quotaBytes)
	}
	return nil
}

// getCacheSizeUnlocked calculates cache size without locking (caller must hold lock)
func (cs *CacheStore) getCacheSizeUnlocked() (int64, error) {
	var totalSize int64
	err := filepath.Walk(cs.cacheDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			totalSize += info.Size()
		}
		return nil
	})
	return totalSize, err
}
