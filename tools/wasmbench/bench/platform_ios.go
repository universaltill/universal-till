//go:build ios

package bench

import "golang.org/x/sys/unix"

// iosDevice reads the hardware identifier (e.g. "iPhone18,2") and the iOS
// version through sysctl, which the app sandbox allows.
func iosDevice() (model, os string) {
	model, err := unix.Sysctl("hw.machine")
	if err != nil {
		model = "unknown"
	}
	v, err := unix.Sysctl("kern.osproductversion")
	if err != nil {
		v = "unknown"
	}
	return model, "iOS " + v
}
