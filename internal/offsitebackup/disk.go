package offsitebackup

import "github.com/universaltill/universal-till/internal/diskspace"

// probeFree is the free space on the disk holding dir. An unsupported
// platform returns the error, and the caller then skips the check rather
// than refuse on a guess.
func probeFree(dir string) (uint64, error) {
	u, err := diskspace.Probe(dir)
	if err != nil {
		return 0, err
	}
	return u.Free, nil
}
