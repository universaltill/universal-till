//go:build darwin || freebsd

package diskspace

import "golang.org/x/sys/unix"

// probe covers macOS, iOS (GOOS=ios builds with the darwin tag) and FreeBSD,
// whose statfs counts blocks of f_bsize bytes. FreeBSD's f_bavail is signed
// and goes negative once root eats into the reserved blocks: clamp it to 0
// rather than wrap to a huge free figure on the fullest disk.
func probe(path string) (Usage, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return Usage{}, err
	}
	unit := uint64(st.Bsize)
	return Usage{Free: uint64(max(int64(st.Bavail), 0)) * unit, Total: st.Blocks * unit}, nil
}
