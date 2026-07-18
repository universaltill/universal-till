package storage

import (
	"crypto/sha256"
	"os"
	"testing"
)

func TestCacheStore_DownloadLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	cs, err := NewCacheStore(tmpDir, 1024*1024)
	if err != nil {
		t.Fatalf("NewCacheStore failed: %v", err)
	}

	pluginID := "test-plugin"
	version := "1.0.0"

	partPath, err := cs.StartDownload(pluginID, version)
	if err != nil {
		t.Fatalf("StartDownload failed: %v", err)
	}

	testData := []byte("test plugin content")
	if err := os.WriteFile(partPath, testData, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	h := sha256.New()
	h.Write(testData)
	checksum := h.Sum(nil)

	if err := cs.PromoteDownload(pluginID, version, checksum); err != nil {
		t.Fatalf("PromoteDownload failed: %v", err)
	}

	artifactPath, exists := cs.GetArtifact(pluginID, version)
	if !exists {
		t.Error("artifact not found after promotion")
	}

	content, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(content) != string(testData) {
		t.Errorf("content mismatch")
	}
}

func TestCacheStore_ChecksumValidation(t *testing.T) {
	tmpDir := t.TempDir()
	cs, err := NewCacheStore(tmpDir, 1024*1024)
	if err != nil {
		t.Fatalf("NewCacheStore failed: %v", err)
	}

	pluginID := "test-plugin"
	version := "1.0.0"

	partPath, err := cs.StartDownload(pluginID, version)
	if err != nil {
		t.Fatalf("StartDownload failed: %v", err)
	}

	testData := []byte("test content")
	if err := os.WriteFile(partPath, testData, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	wrongChecksum := make([]byte, 32)
	err = cs.PromoteDownload(pluginID, version, wrongChecksum)
	if err == nil {
		t.Error("expected checksum error, got nil")
	}
}
