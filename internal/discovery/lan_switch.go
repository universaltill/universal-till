package discovery

import "sync/atomic"

// lanDisabled is the process-wide LAN-discovery switch (ut-docs#3814). When
// set, this till neither advertises over mDNS nor browses it, so a throwaway
// e2e/docs-shots till is invisible to real tills on the developer's LAN and
// never shows one of them in its own banners.
var lanDisabled atomic.Bool

// SetLANDisabled turns LAN discovery off (true) or back on (false). app.Run
// sets it once from cfg.LANDiscovery (UT_LAN_DISCOVERY), like
// netaccess.SetDemo, and clears it on return.
func SetLANDisabled(on bool) { lanDisabled.Store(on) }

// LANDisabled reports whether LAN discovery is switched off.
func LANDisabled() bool { return lanDisabled.Load() }
