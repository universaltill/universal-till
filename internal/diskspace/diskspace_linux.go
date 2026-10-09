//go:build linux

package diskspace

import "golang.org/x/sys/unix"

// probe covers Linux and Android (GOOS=android builds with the linux tag).
// f_bavail and f_blocks count fragments of f_frsize bytes; f_bsize is only
// the preferred I/O size, so prefer f_frsize when the kernel reports it.
func probe(path string) (Usage, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return Usage{}, err
	}
	unit := uint64(st.Frsize)
	if unit == 0 {
		unit = uint64(st.Bsize)
	}
	return Usage{Free: st.Bavail * unit, Total: st.Blocks * unit}, nil
}
