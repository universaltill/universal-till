package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/diskspace"
	"github.com/universaltill/universal-till/internal/updates"
)

// Offline (USB / file) install, ut-docs#5438 and the ADR-0178 amendment of
// 2026-11-04. A till with no internet installs a release the operator brings
// as three files: the tar.gz archive, checksums.txt and checksums.txt.sig. It
// is the same trust chain as the online path: verifySums (the one
// releasesig.Verify call), then the SHA-256 of the archive against the SIGNED
// line for its name, then the shared installArchive tail. Unlike the online
// path it refuses when this build is not armed with release keys: offline,
// the operator supplies both the artifact and its checksum, so SHA-256 alone
// would prove nothing. No network call is made.

var (
	ErrFileNotArmed    = errors.New("this till's build can't check update signatures, so file updates are off")
	ErrFileUnsupported = errors.New("file install isn't available on this kind of install yet")
	ErrFileBadName     = errors.New("not an update archive for this till")
	ErrFileNotNewer    = errors.New("update is not newer than this build")
	ErrFileSameVersion = errors.New("this till already runs that version")
	ErrFileChecksum    = errors.New("archive does not match the signed checksums")
	ErrFileTooLarge    = errors.New("update file too large")
	ErrFileNoSpace     = errors.New("not enough free disk space for the update")
)

const (
	// MaxFileArchiveBytes caps the uploaded archive (a release is ~80 MB).
	MaxFileArchiveBytes int64 = 256 << 20
	// MaxFileChecksumsBytes caps checksums.txt (a release's is ~1.5 KB).
	MaxFileChecksumsBytes int64 = 1 << 20
	// MaxFileSigBytes is the read cap for checksums.txt.sig; releasesig
	// rejects more than 16 KiB as malformed.
	MaxFileSigBytes int64 = 16<<10 + 1
)

// fileArchiveRe is the only filename sanitiser: an exact match, so a path, a
// traversal or a browser "(1)" suffix is simply "bad name".
var fileArchiveRe = regexp.MustCompile(`^unitill-pos_([0-9]+(?:\.[0-9]+){1,3})_` +
	regexp.QuoteMeta(runtime.GOOS) + `_` + regexp.QuoteMeta(runtime.GOARCH) + `\.tar\.gz$`)

// FileArchiveSuffix is "_<goos>_<goarch>.tar.gz", shown in the UI so the
// operator picks the right file.
func FileArchiveSuffix() string {
	return "_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
}

// FileArchiveNameVersion is the version in an archive name that matches this
// till's exact pattern, else "". It checks the name only (no newer-than rule),
// so a caller can word a refusal with the file's version.
func FileArchiveNameVersion(name string) string {
	if m := fileArchiveRe.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	return ""
}

// FileArchiveVersion validates an uploaded archive's name (exact match) and
// returns its version. The anti-downgrade rule of ADR-0178 section 8 applies:
// the version must be newer than this build.
func FileArchiveVersion(name string) (string, error) {
	v := FileArchiveNameVersion(name)
	if v == "" {
		return "", ErrFileBadName
	}
	if v == buildinfo.Version {
		return "", fmt.Errorf("%w: v%s", ErrFileSameVersion, v)
	}
	if !updates.Newer(v, buildinfo.Version) {
		return "", fmt.Errorf("%w: v%s is not newer than v%s", ErrFileNotNewer, v, buildinfo.Version)
	}
	return v, nil
}

// FileInstallAvailability is the render-time and request-time gate. It has no
// side effects. Reasons are ordered by what survives arming (UX R-UX4): an
// install that can never self-update, then a kind of install this card does
// not cover, then an unarmed build.
func FileInstallAvailability() error {
	if !Supported() {
		return ErrUnsupported
	}
	if hostGOOS == "windows" {
		return ErrFileUnsupported
	}
	if hostGOOS == "darwin" {
		if exe, err := osExecutable(); err == nil && appBundlePath(exe) != "" {
			return ErrFileUnsupported
		}
	}
	_, armed, err := compiledReleaseKeys()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrFileNotArmed, err)
	}
	if !armed {
		return ErrFileNotArmed
	}
	return nil
}

// FileInstall is one file-install attempt. It holds applyMu from Begin to
// Close, so an online Apply, the follow loop or the nightly update answers
// ErrBusy meanwhile, and the reverse.
type FileInstall struct {
	dir, exe, webBase string
	closed            sync.Once
}

// BeginFileInstall checks availability, takes applyMu (before any upload byte
// is read), and creates the private temp dir. The caller must Close.
func BeginFileInstall() (*FileInstall, error) {
	if err := FileInstallAvailability(); err != nil {
		return nil, err
	}
	if windowsHandover.Load() || !applyMu.TryLock() {
		return nil, ErrBusy
	}
	exe, err := osExecutable()
	if err != nil {
		applyMu.Unlock()
		return nil, fmt.Errorf("locate executable: %w", err)
	}
	dir, err := os.MkdirTemp("", "ut-update-file-*")
	if err != nil {
		applyMu.Unlock()
		return nil, err
	}
	return &FileInstall{dir: dir, exe: exe, webBase: serverWebBase(filepath.Dir(exe))}, nil
}

// Dir is the private directory the archive must be written into.
func (f *FileInstall) Dir() string { return f.dir }

// fileArchiveCap is the archive size cap for the disk holding dir: at most
// MaxFileArchiveBytes and at most a quarter of the space left above the
// low-disk floor (the archive plus its extracted stage is about three times
// its size). 0 = no room; an unknown probe leaves the fixed cap. A var so
// tests can pin it.
var fileArchiveCap = func(dir string) int64 {
	u, err := diskspace.Probe(dir)
	if err != nil {
		return MaxFileArchiveBytes
	}
	floor := diskspace.Floor(u.Total)
	if u.Free <= floor {
		return 0
	}
	return min(MaxFileArchiveBytes, int64((u.Free-floor)/4))
}

// ArchiveCap is the most archive bytes the handler may accept (0 = no room).
func (f *FileInstall) ArchiveCap() int64 { return fileArchiveCap(f.dir) }

// FileSet is the three uploaded files; the archive is already on disk.
type FileSet struct {
	ArchiveName, ArchivePath string
	Sums, Sig                []byte
}

// Installed reports what a successful Install put in place.
type Installed struct{ Version, KeyID string }

// Install runs the fixed verification order; nothing is extracted or swapped
// before the signature and the SHA-256 have both passed:
//  1. availability again (armed + platform; defence in depth)
//  2. version from the archive name (+ anti-downgrade)
//  3. signature over the exact checksums.txt bytes
//  4. SHA-256 of the archive against the signed line for that name
//  5. the shared extract -> swap -> smoke-run -> rollback -> restart tail
func (f *FileInstall) Install(ctx context.Context, in FileSet) (Installed, error) {
	if err := FileInstallAvailability(); err != nil {
		return Installed{}, err
	}
	rel, err := filepath.Rel(f.dir, in.ArchivePath)
	if err != nil || rel != filepath.Base(in.ArchivePath) || rel == "." || rel == ".." {
		return Installed{}, fmt.Errorf("archive %q is not inside the private update directory", in.ArchivePath)
	}
	v, err := FileArchiveVersion(in.ArchiveName)
	if err != nil {
		return Installed{}, err
	}
	keys, _, err := compiledReleaseKeys()
	if err != nil {
		return Installed{}, fmt.Errorf("%w: %w", ErrFileNotArmed, err)
	}
	s, keyID, err := verifySums(keys, v, in.Sums, in.Sig)
	if err != nil {
		return Installed{}, err
	}
	want, err := s.lookup(in.ArchiveName)
	if err != nil {
		return Installed{}, fmt.Errorf("%w: %v", ErrFileChecksum, err)
	}
	if err := verifySHA256(in.ArchivePath, want); err != nil {
		return Installed{}, fmt.Errorf("%w: %v", ErrFileChecksum, err)
	}
	if err := installArchive(ctx, f.exe, f.webBase, f.dir, in.ArchivePath, v, nil); err != nil {
		return Installed{}, err
	}
	return Installed{Version: v, KeyID: keyID}, nil
}

// Close removes Dir() and releases applyMu. Idempotent.
func (f *FileInstall) Close() {
	f.closed.Do(func() {
		_ = os.RemoveAll(f.dir)
		applyMu.Unlock()
	})
}
