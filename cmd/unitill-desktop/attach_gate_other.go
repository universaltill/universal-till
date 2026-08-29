//go:build !(desktop && linux)

package main

import "time"

// attachDeadline is a no-op off the desktop&&linux path, mirroring
// startup_gate_other.go's waitForSafeStartup: ut-docs#1093's cold-boot
// startup gate is a WebKitGTK-on-Wayland-specific mitigation, so there is no
// window for main()'s attach probe (desktop.go) to retry across on macOS or
// Windows — decide from a single probe immediately, the same behaviour this
// platform always had (ut-docs#1199).
func attachDeadline() time.Time { return time.Now() }
