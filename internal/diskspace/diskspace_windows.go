//go:build windows

package diskspace

import "golang.org/x/sys/windows"

// probe uses GetDiskFreeSpaceEx; freeAvail honours per-user disk quotas.
func probe(path string) (Usage, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return Usage{}, err
	}
	var freeAvail, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &freeAvail, &total, &totalFree); err != nil {
		return Usage{}, err
	}
	return Usage{Free: freeAvail, Total: total}, nil
}
