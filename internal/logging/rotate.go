package logging

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// Log-file budget (ut-docs#2720). The till's disk use for logs is bounded:
// at most DefaultMaxFiles files of DefaultMaxFileBytes each (25 MB total),
// the oldest discarded first. Nothing is buffered in memory — each Write
// goes straight to the file — so the binding "till caches/buffers are
// memory-bounded" rule holds trivially.
// renameFile is os.Rename; a test seam for the Windows sharing-violation
// fallback in rotate.
var renameFile = os.Rename

const (
	DefaultMaxFileBytes = 5 << 20
	DefaultMaxFiles     = 5
)

// RotatingWriter is a size-based rotating log file: till.log is the live
// file, till.log.1 the previous one, … till.log.<maxFiles-1> the oldest.
// Safe for concurrent use. Hand-rolled rather than a dependency (no
// rotating writer is vendored) — it is ~80 lines and fully tested.
type RotatingWriter struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	maxFiles int
	f        *os.File
	size     int64
}

// NewRotatingWriter opens (creating, with its directory) path for append.
// maxFiles counts the live file, so 5 means till.log + 4 rotated files.
func NewRotatingWriter(path string, maxBytes int64, maxFiles int) (*RotatingWriter, error) {
	if maxBytes <= 0 || maxFiles < 1 {
		return nil, fmt.Errorf("logging: invalid rotation budget %d×%d", maxFiles, maxBytes)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("logging: create log dir: %w", err)
	}
	w := &RotatingWriter{path: path, maxBytes: maxBytes, maxFiles: maxFiles}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

// open (re)opens the live file for append and records its current size.
// 0o600: a log is operational detail for the shop's own operator, not for
// every local account.
func (w *RotatingWriter) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("logging: open log file: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("logging: stat log file: %w", err)
	}
	w.f, w.size = f, info.Size()
	return nil
}

// Write appends p, rotating first when p would take the live file past
// maxBytes. A single write larger than maxBytes is truncated to it, so no
// write can break the budget.
func (w *RotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	if int64(len(p)) > w.maxBytes {
		p = p[:w.maxBytes]
	}
	if w.size > 0 && w.size+int64(len(p)) > w.maxBytes {
		w.rotate()
	}
	if w.f == nil {
		// A previous reopen failed; try once more, else drop the line —
		// logging must never take the till down.
		if err := w.open(); err != nil {
			return n, err
		}
	}
	written, err := w.f.Write(p)
	w.size += int64(written)
	if err != nil {
		return written, err
	}
	return n, nil
}

// rotate shifts till.log.i → till.log.i+1 (dropping the oldest) and starts
// a fresh live file. Rename fails on Windows while another program holds
// the live file open without FILE_SHARE_DELETE; the fallback copies it
// (at most maxBytes) to till.log.1 and then truncates it, so the newest
// content survives and the budget still holds.
func (w *RotatingWriter) rotate() {
	if w.f != nil {
		_ = w.f.Close()
		w.f = nil
	}
	name := func(i int) string {
		if i == 0 {
			return w.path
		}
		return fmt.Sprintf("%s.%d", w.path, i)
	}
	if w.maxFiles == 1 {
		if err := os.Remove(w.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = os.Truncate(w.path, 0)
		}
	} else {
		_ = os.Remove(name(w.maxFiles - 1))
		for i := w.maxFiles - 2; i >= 1; i-- {
			_ = renameFile(name(i), name(i+1))
		}
		if err := renameFile(w.path, name(1)); err != nil && !errors.Is(err, os.ErrNotExist) {
			w.copyThenTruncate(name(1))
		}
	}
	w.size = 0
	_ = w.open() // on failure w.f stays nil; Write retries
}

// copyThenTruncate is rotate's fallback when the live file can't be
// renamed: copy at most maxBytes of it to dst, then empty it. If the copy
// fails the live file is still truncated — the budget wins over one file.
func (w *RotatingWriter) copyThenTruncate(dst string) {
	func() {
		src, err := os.Open(w.path)
		if err != nil {
			return
		}
		defer src.Close()
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return
		}
		defer out.Close()
		_, _ = io.CopyN(out, src, w.maxBytes)
	}()
	_ = os.Truncate(w.path, 0)
}

// Close closes the live file.
func (w *RotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}
