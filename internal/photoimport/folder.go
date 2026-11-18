package photoimport

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
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

// ReadInFolder resolves ref inside root and reads it through an os.Root, so
// a path component swapped for a symlink between the check and the open
// cannot leave the folder (the kernel-level confinement is the real guard;
// ResolveInFolder only classifies the refusal).
func ReadInFolder(root, ref string, max int64) ([]byte, error) {
	p, err := ResolveInFolder(root, ref)
	if err != nil {
		return nil, err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, ErrNotFound
	}
	rel, err := filepath.Rel(realRoot, p)
	if err != nil {
		return nil, ErrOutside
	}
	if runtime.GOOS == "windows" && reservedDeviceName(rel) {
		return nil, ErrNotRegular
	}
	rt, err := os.OpenRoot(realRoot)
	if err != nil {
		return nil, ErrNotFound
	}
	defer rt.Close()
	st, err := rt.Lstat(rel)
	if err != nil {
		return nil, ErrNotFound
	}
	if !st.Mode().IsRegular() { // before open: opening a FIFO would block
		return nil, ErrNotRegular
	}
	f, err := rt.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrNotFound
	}
	defer f.Close()
	return readHandle(f, max)
}

// reservedDeviceName reports a Windows device name (CON, NUL, COM1, ...) as
// any path component, with or without an extension (Windows only: elsewhere
// those are ordinary file names and the file-type check covers devices).
func reservedDeviceName(rel string) bool {
	for _, part := range strings.FieldsFunc(rel, func(r rune) bool { return r == '/' || r == '\\' }) {
		stem := strings.ToUpper(strings.TrimRight(strings.SplitN(part, ".", 2)[0], " "))
		switch stem {
		case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
			return true
		}
		if len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '0' && stem[3] <= '9' {
			return true
		}
	}
	return false
}

func readHandle(f *os.File, max int64) ([]byte, error) {
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

// ValidateFolder refuses a photos folder that is not a plain local directory:
// UNC / device roots (an SMB connect would authenticate to a remote host),
// filesystem and drive roots, and system trees that never hold shop photos.
func ValidateFolder(folder string) error {
	n := strings.ReplaceAll(folder, `\`, "/")
	if strings.HasPrefix(n, "//") || strings.ContainsRune(folder, 0) {
		return ErrOutside
	}
	if !filepath.IsAbs(folder) {
		return ErrOutside
	}
	real, err := filepath.EvalSymlinks(folder)
	if err != nil {
		return ErrNotFound
	}
	real = filepath.Clean(real)
	if filepath.Dir(real) == real { // "/" or a drive root
		// A removable drive root (E:\ on a USB stick) is fine; "/" and the
		// system drive root are not.
		sys := os.Getenv("SystemDrive")
		if runtime.GOOS != "windows" || sys == "" || strings.EqualFold(filepath.VolumeName(real), sys) {
			return ErrOutside
		}
	}
	low := strings.ToLower(filepath.ToSlash(real))
	for _, bad := range []string{"/dev", "/proc", "/sys", "/etc", "/boot", "/root", "/var/lib", "/private/etc", "/system", "/library", "/windows", "/program files"} {
		if low == bad || strings.HasPrefix(low, bad+"/") || (len(low) > 2 && low[1] == ':' && (low[2:] == bad || strings.HasPrefix(low[2:], bad+"/"))) {
			return ErrOutside
		}
	}
	st, err := os.Stat(real)
	if err != nil || !st.IsDir() {
		return ErrNotFound
	}
	return nil
}
