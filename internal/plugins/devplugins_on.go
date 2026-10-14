//go:build devplugins

package plugins

// allowUnsignedPlugins is true only in a `-tags devplugins` build (ADR-0158):
// with no marketplace key the verifier does structural checks only, and
// skip_signature on import is honoured. Never used for a shipped binary.
const allowUnsignedPlugins = true
