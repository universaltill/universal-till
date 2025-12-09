package plugins

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/universaltill/universal-till/internal/logging"
)

// ImportFormat represents supported plugin archive formats
type ImportFormat string

const (
	ImportFormatZip   ImportFormat = "zip"
	ImportFormatTarGz ImportFormat = "tar.gz"
)

// ImportRequest contains details for manual plugin import
type ImportRequest struct {
	FilePath      string       // Path to the plugin archive file
	Format        ImportFormat // Archive format (zip or tar.gz)
	MaxSizeBytes  int64        // Maximum allowed archive size
	TrustLevel    string       // Trust level to assign (untrusted, trusted, verified)
	Uploader      string       // User/actor performing the import
	SkipSignature bool         // Skip signature verification (dev-mode only)
}

// ImportResult contains the outcome of an import operation
type ImportResult struct {
	PluginID      string
	Version       string
	Name          string
	ExtractedPath string
	Manifest      *Manifest
	Warnings      []string
}

// Importer handles manual plugin imports from local files
type Importer struct {
	db             *sql.DB
	pluginBaseDir  string
	verifier       *ManifestVerifier
	manifestParser func(io.Reader) (*Manifest, error)
}

// NewImporter creates a new plugin importer
func NewImporter(db *sql.DB, pluginBaseDir string, verifier *ManifestVerifier) *Importer {
	return &Importer{
		db:             db,
		pluginBaseDir:  pluginBaseDir,
		verifier:       verifier,
		manifestParser: ParseManifest,
	}
}

// Import performs manual plugin import from a local file (T027)
func (imp *Importer) Import(ctx context.Context, req *ImportRequest) (*ImportResult, error) {
	log := logging.L()

	// Validate request
	if req.FilePath == "" {
		return nil, fmt.Errorf("file path is required")
	}

	if req.MaxSizeBytes <= 0 {
		req.MaxSizeBytes = 200 * 1024 * 1024 // Default 200 MB
	}

	// Check file exists and size
	fileInfo, err := os.Stat(req.FilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}

	if fileInfo.Size() > req.MaxSizeBytes {
		return nil, fmt.Errorf("file size %d exceeds maximum %d bytes", fileInfo.Size(), req.MaxSizeBytes)
	}

	// Detect format if not specified
	if req.Format == "" {
		req.Format = detectFormat(req.FilePath)
	}

	// Create temporary extraction directory
	tempDir := filepath.Join(imp.pluginBaseDir, "tmp", fmt.Sprintf("import-%d", os.Getpid()))
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	// Extract archive
	log.Infof("[Importer] Extracting %s archive: %s", req.Format, req.FilePath)
	if err := imp.extractArchive(req.FilePath, tempDir, req.Format); err != nil {
		return nil, fmt.Errorf("failed to extract archive: %w", err)
	}

	// Parse and validate manifest
	manifestPath := filepath.Join(tempDir, "manifest.json")
	manifestFile, err := os.Open(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("manifest.json not found in archive: %w", err)
	}
	defer manifestFile.Close()

	manifest, err := imp.manifestParser(manifestFile)
	if err != nil {
		return nil, fmt.Errorf("invalid manifest: %w", err)
	}

	// Verify manifest (signature check can be skipped in dev-mode)
	if !req.SkipSignature && imp.verifier != nil {
		manifestFile.Close()
		_, err := imp.verifier.VerifyManifest(manifestPath)
		if err != nil {
			return nil, fmt.Errorf("manifest verification failed: %w", err)
		}
		// Reopen for parsing
		manifestFile, err = os.Open(manifestPath)
		if err != nil {
			return nil, fmt.Errorf("failed to reopen manifest: %w", err)
		}
	}

	result := &ImportResult{
		PluginID: manifest.ID,
		Version:  manifest.Version,
		Name:     manifest.Name,
		Manifest: manifest,
		Warnings: []string{},
	}

	if req.SkipSignature {
		result.Warnings = append(result.Warnings, "Signature verification skipped (dev-mode)")
	}

	// Check disk budget (prevent excessive plugin storage)
	if err := imp.checkDiskBudget(manifest.ID, manifest.Version, tempDir); err != nil {
		return nil, fmt.Errorf("disk budget check failed: %w", err)
	}

	// Move to permanent location
	permanentDir := filepath.Join(imp.pluginBaseDir, manifest.ID, manifest.Version)
	if err := os.RemoveAll(permanentDir); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to clean existing directory: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(permanentDir), 0755); err != nil {
		return nil, fmt.Errorf("failed to create plugin directory: %w", err)
	}

	if err := os.Rename(tempDir, permanentDir); err != nil {
		return nil, fmt.Errorf("failed to move plugin to permanent location: %w", err)
	}

	result.ExtractedPath = permanentDir

	// Persist to database
	installOpts := InstallOptions{
		InstalledFromURL: fmt.Sprintf("file://%s", req.FilePath),
		SHA256:           "", // Not available for manual imports
		TrustLevel:       req.TrustLevel,
		Uploader:         req.Uploader,
	}

	if err := PersistManifest(ctx, imp.db, manifest, installOpts); err != nil {
		// Cleanup on database failure
		os.RemoveAll(permanentDir)
		return nil, fmt.Errorf("failed to persist plugin: %w", err)
	}

	log.Infof("[Importer] Successfully imported plugin %s v%s", manifest.Name, manifest.Version)
	return result, nil
}

// extractArchive extracts a plugin archive to the destination directory
func (imp *Importer) extractArchive(archivePath, destDir string, format ImportFormat) error {
	switch format {
	case ImportFormatZip:
		return imp.extractZip(archivePath, destDir)
	case ImportFormatTarGz:
		return imp.extractTarGz(archivePath, destDir)
	default:
		return fmt.Errorf("unsupported archive format: %s", format)
	}
}

// extractZip extracts a ZIP archive with security checks
func (imp *Importer) extractZip(archivePath, destDir string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("failed to open zip: %w", err)
	}
	defer reader.Close()

	for _, file := range reader.File {
		target := filepath.Join(destDir, file.Name)

		// Security: prevent path traversal
		if !strings.HasPrefix(target, filepath.Clean(destDir)+string(os.PathSeparator)) {
			return fmt.Errorf("illegal file path in archive: %s", file.Name)
		}

		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return fmt.Errorf("failed to create directory: %w", err)
			}
			continue
		}

		// Create parent directories
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return fmt.Errorf("failed to create parent directory: %w", err)
		}

		// Extract file
		outFile, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR|os.O_TRUNC, file.Mode())
		if err != nil {
			return fmt.Errorf("failed to create file: %w", err)
		}

		rc, err := file.Open()
		if err != nil {
			outFile.Close()
			return fmt.Errorf("failed to open file in archive: %w", err)
		}

		_, err = io.Copy(outFile, rc)
		rc.Close()
		outFile.Close()

		if err != nil {
			return fmt.Errorf("failed to write file: %w", err)
		}
	}

	return nil
}

// extractTarGz extracts a tar.gz archive with security checks
func (imp *Importer) extractTarGz(archivePath, destDir string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("failed to open archive: %w", err)
	}
	defer file.Close()

	gzReader, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer gzReader.Close()

	tarReader := tar.NewReader(gzReader)

	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read tar entry: %w", err)
		}

		target := filepath.Join(destDir, header.Name)

		// Security: prevent path traversal
		if !strings.HasPrefix(target, filepath.Clean(destDir)+string(os.PathSeparator)) {
			return fmt.Errorf("illegal file path in archive: %s", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return fmt.Errorf("failed to create directory: %w", err)
			}
		case tar.TypeReg:
			dir := filepath.Dir(target)
			if err := os.MkdirAll(dir, 0755); err != nil {
				return fmt.Errorf("failed to create parent directory: %w", err)
			}

			outFile, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR, os.FileMode(header.Mode))
			if err != nil {
				return fmt.Errorf("failed to create file: %w", err)
			}

			if _, err := io.Copy(outFile, tarReader); err != nil {
				outFile.Close()
				return fmt.Errorf("failed to write file: %w", err)
			}
			outFile.Close()
		}
	}

	return nil
}

// checkDiskBudget verifies disk space constraints
func (imp *Importer) checkDiskBudget(pluginID, version, extractedPath string) error {
	// Calculate size of extracted plugin
	var totalSize int64
	err := filepath.Walk(extractedPath, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			totalSize += info.Size()
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to calculate plugin size: %w", err)
	}

	// Check total plugin directory size
	pluginDir := filepath.Join(imp.pluginBaseDir, pluginID)
	var existingSize int64
	if _, err := os.Stat(pluginDir); err == nil {
		filepath.Walk(pluginDir, func(_ string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				existingSize += info.Size()
			}
			return nil
		})
	}

	// Simple budget: max 1 GB per plugin (including all versions)
	maxPluginSize := int64(1024 * 1024 * 1024) // 1 GB
	if existingSize+totalSize > maxPluginSize {
		return fmt.Errorf("plugin storage would exceed 1 GB limit (current: %d, new: %d)", existingSize, totalSize)
	}

	return nil
}

// detectFormat detects archive format from file extension
func detectFormat(filePath string) ImportFormat {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".zip":
		return ImportFormatZip
	case ".gz":
		// Check if it's .tar.gz
		if strings.HasSuffix(strings.ToLower(filePath), ".tar.gz") {
			return ImportFormatTarGz
		}
		return ImportFormatTarGz
	default:
		// Default to tar.gz for .utplugin or unknown extensions
		return ImportFormatTarGz
	}
}
