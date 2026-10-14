package plugins

// UnsignedPluginsAllowed reports whether this binary was built with
// `-tags devplugins` (ADR-0158) — the only way unsigned plugins can run. No
// setting, env var or UI changes it.
func UnsignedPluginsAllowed() bool { return allowUnsignedPlugins }
