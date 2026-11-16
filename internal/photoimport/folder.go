package photoimport

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// MaxImageBytes is the per-image cap (the item editor's upload cap).
const MaxImageBytes = 10 << 20

var (
	// ErrOutside: the path is not strictly inside the chosen folder.
	ErrOutside = errors.New("photoimport: path outside the photos folder")
	// ErrNotFound: nothing readable there.
	ErrNotFound = errors.New("photoimport: file not found")
	// ErrTooBig: the file is over the size cap.
	ErrTooBig = errors.New("photoimport: file too big")
	// ErrNotRegular: not a regular file (directory, device, pipe).
	ErrNotRegular = errors.New("photoimport: not a regular file")
)

// ResolveInFolder maps a CSV path (relative to root, or absolute but inside
// it; backslashes accepted) to a real file path inside root. Symlinks are
// resolved before the containment check, so a link out of the folder is
// refused. ".." components, UNC paths and NUL bytes are refused outright.
func ResolveInFolder(root, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if root == "" || ref == "" || strings.ContainsRune(ref, 0) {
		return "", ErrOutside
	}
	norm := strings.ReplaceAll(ref, `\`, "/")
	if strings.HasPrefix(norm, "//") {
		return "", ErrOutside // UNC
	}
	for _, part := range strings.Split(norm, "/") {
		if part == ".." {
			return "", ErrOutside
		}
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", ErrNotFound
	}
	realRoot, err = filepath.Abs(realRoot)
	if err != nil {
		return "", ErrNotFound
	}
	var cand string
	if strings.HasPrefix(norm, "/") || filepath.IsAbs(filepath.FromSlash(norm)) {
		cand = filepath.FromSlash(norm)
	} else {
		cand = filepath.Join(realRoot, filepath.FromSlash(norm))
	}
	real, err := filepath.EvalSymlinks(cand)
	if err != nil {
		// A missing file inside the folder is "not found"; an absolute path
		// that is not even lexically inside is "outside".
		if !within(realRoot, filepath.Clean(cand)) && !within(root, filepath.Clean(cand)) {
			return "", ErrOutside
		}
		return "", ErrNotFound
	}
	if !within(realRoot, real) {
		return "", ErrOutside
	}
	return real, nil
}

// within reports whether p is strictly below root.
func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return false
	}
	return true
}

// ReadRegular reads a regular file of at most max bytes. The type check is
// made on the open handle, so a swap between resolve and open cannot turn it
// into a device or pipe read.
func ReadRegular(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, ErrNotFound
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, ErrNotFound
	}
	if !st.Mode().IsRegular() {
		return nil, ErrNotRegular
	}
	if st.Size() > max {
		return nil, ErrTooBig
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, ErrNotFound
	}
	if int64(len(b)) > max {
		return nil, ErrTooBig
	}
	return b, nil
}
