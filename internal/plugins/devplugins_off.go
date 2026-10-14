//go:build !devplugins

package plugins

// allowUnsignedPlugins is false in every normal build (ADR-0158): a verifier
// without a marketplace key refuses every manifest, and skip_signature on
// import is ignored. Only a `-tags devplugins` build turns it on.
const allowUnsignedPlugins = false
