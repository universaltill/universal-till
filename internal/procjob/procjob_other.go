//go:build !windows

package procjob

import "os"

func killWithParent(*os.Process) error { return nil }
