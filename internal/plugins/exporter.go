package plugins

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/universaltill/universal-till/internal/logging"
)

// ExportRequest describes an installed plugin to package into a portable offline
// bundle.
type ExportRequest struct {
	PluginID string // plugin manifest ID (e.g. com.universaltill.ut-faq)
	Version  string // installed version to export
	DestPath string // where to write the .tar.gz bundle
}

// ExportResult summarizes a completed export.
type ExportResult struct {
	PluginID string
	Version  string
	Path     string
	Size     int64  // bytes of the written bundle
	SHA256   string // checksum of the written bundle (hex)
}

// Exporter packages an installed plugin directory into a .tar.gz bundle that the
// Importer can re-import on an offline till (device provisioning / migration
// without marketplace connectivity). It reuses the on-disk install layout —
// manifest.json at the root plus the plugin's files — so an export round-trips
// straight back through Importer.Import.
type Exporter struct {
	pluginBaseDir string
}

// NewExporter creates a plugin exporter rooted at the plugin base directory
// (the same directory the Importer/installer use).
func NewExporter(pluginBaseDir string) *Exporter {
	return &Exporter{pluginBaseDir: pluginBaseDir}
}

// Export writes the installed plugin at {baseDir}/{id}/{version} to req.DestPath
// as a gzip-compressed tar. The archive is rooted at the plugin directory, so
// manifest.json sits at the archive root — exactly what Importer.Import expects.
func (e *Exporter) Export(ctx context.Context, req *ExportRequest) (*ExportResult, error) {
	if req.PluginID == "" || req.Version == "" {
		return nil, fmt.Errorf("plugin id and version are required")
	}
	if req.DestPath == "" {
		return nil, fmt.Errorf("destination path is required")
	}

	srcDir := filepath.Join(e.pluginBaseDir, req.PluginID, req.Version)
	info, err := os.Stat(srcDir)
	if err != nil {
		return nil, fmt.Errorf("plugin %s v%s not found on disk: %w", req.PluginID, req.Version, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("plugin path is not a directory: %s", srcDir)
	}
	// A bundle is only importable if it carries the manifest at its root.
	if _, err := os.Stat(filepath.Join(srcDir, "manifest.json")); err != nil {
		return nil, fmt.Errorf("manifest.json missing in %s: %w", srcDir, err)
	}

	out, err := os.Create(req.DestPath)
	if err != nil {
		return nil, fmt.Errorf("create bundle: %w", err)
	}
	defer out.Close()

	// Hash the compressed bytes as we write them, so the caller can record the
	// bundle checksum without a second read.
	hasher := sha256.New()
	gz := gzip.NewWriter(io.MultiWriter(out, hasher))
	tw := tar.NewWriter(gz)

	writeErr := filepath.Walk(srcDir, func(path string, fi os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		// Only emit entry types the Importer restores (dirs + regular files);
		// skip symlinks/devices rather than embed something it will ignore.
		if !fi.IsDir() && !fi.Mode().IsRegular() {
			return nil
		}
		hdr, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			return fmt.Errorf("tar header for %s: %w", rel, err)
		}
		hdr.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("write tar header %s: %w", rel, err)
		}
		if fi.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := io.Copy(tw, f); err != nil {
			return fmt.Errorf("copy %s: %w", rel, err)
		}
		return nil
	})

	// Close writers in order; a walk error still needs them closed before we can
	// remove the partial file.
	tarCloseErr := tw.Close()
	gzCloseErr := gz.Close()
	if err := firstErr(writeErr, tarCloseErr, gzCloseErr); err != nil {
		out.Close()
		os.Remove(req.DestPath)
		return nil, fmt.Errorf("archive plugin: %w", err)
	}
	if err := out.Close(); err != nil {
		os.Remove(req.DestPath)
		return nil, fmt.Errorf("finalize bundle: %w", err)
	}

	st, err := os.Stat(req.DestPath)
	if err != nil {
		return nil, fmt.Errorf("stat bundle: %w", err)
	}

	logging.L().Infof("[Exporter] Exported plugin %s v%s -> %s (%d bytes)", req.PluginID, req.Version, req.DestPath, st.Size())
	return &ExportResult{
		PluginID: req.PluginID,
		Version:  req.Version,
		Path:     req.DestPath,
		Size:     st.Size(),
		SHA256:   hex.EncodeToString(hasher.Sum(nil)),
	}, nil
}

// firstErr returns the first non-nil error, or nil.
func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
