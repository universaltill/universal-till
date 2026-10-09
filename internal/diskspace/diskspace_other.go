//go:build !linux && !darwin && !freebsd && !windows

package diskspace

func probe(string) (Usage, error) { return Usage{}, ErrUnsupported }
