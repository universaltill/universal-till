package plugins

import (
	"errors"
	"fmt"
	"sync"

	"github.com/universaltill/universal-till/internal/buildinfo"
)

// Apple guideline 2.5.2 (ADR-0194): the App Store build never downloads code.
// Marketplace plugins that run code (runtime wasm or go) are refused on that
// channel by every install, import, update, cloud-directive and replica path;
// the first-party Stripe and SumUp payment plugins ship inside the app
// (internal/plugins/bundled) and are only switched on and off. Asset-only
// plugins (runtime "none": themes, languages, layouts) are unaffected.
//
// The channel is read from buildinfo.CurrentChannel, a build constant, never
// from settings, env or a request.

// ErrCodePluginRefused is returned wherever a code plugin install is refused
// by the channel. Callers match it with errors.Is.
var ErrCodePluginRefused = errors.New("code plugins cannot be installed on this build")

// CodeInstallRefusedMsgID is the translated message shown for ErrCodePluginRefused.
const CodeInstallRefusedMsgID = "plugins.install.error.code_refused_appstore"

// assetOnlyCanonicalTypes are the catalog types that ship no code, so the
// runtime is knowable from the catalog row before anything is fetched.
var assetOnlyCanonicalTypes = map[string]bool{"theme": true, "language": true, "layout": true}

func codeInstallAllowed() bool { return buildinfo.CurrentChannel().CodePluginInstallAllowed() }

// RefuseCodeRuntime is the gate by manifest runtime: nil on a channel that
// allows code plugins, and for runtime "none" everywhere.
func RefuseCodeRuntime(runtime string) error {
	if codeInstallAllowed() || runtime == "none" {
		return nil
	}
	return fmt.Errorf("%w (runtime %q)", ErrCodePluginRefused, runtime)
}

// CodeRuntimeRefused reports whether RefuseCodeRuntime would refuse runtime on
// this build (ut-docs#7017: the plugin list explains a broken code plugin that
// the channel will never reinstall). An empty runtime fails closed like the
// gate itself: only "none" passes on a channel that forbids code.
func CodeRuntimeRefused(runtime string) bool { return RefuseCodeRuntime(runtime) != nil }

// RefuseCodeCanonicalType is the pre-network gate by catalog type: only the
// asset-only types pass on a channel that forbids code. An unknown or empty
// type fails closed.
func RefuseCodeCanonicalType(canonicalType string) error {
	if codeInstallAllowed() || assetOnlyCanonicalTypes[canonicalType] {
		return nil
	}
	return fmt.Errorf("%w (type %q)", ErrCodePluginRefused, canonicalType)
}

// BundledCodeVerifier says whether wasm (the exact bytes about to be
// compiled, read from path) is the first-party bundled plugin id@version,
// byte-for-byte as pinned in the compiled-in lock. It judges the bytes it is
// handed, never a second read of the file, so nothing can be swapped between
// the check and the load.
type BundledCodeVerifier func(id, version, path string, wasm []byte) bool

var (
	bundledMu       sync.RWMutex
	bundledVerifier BundledCodeVerifier
)

// SetBundledCodeVerifier is called once at boot by internal/plugins/bundled
// (it cannot be imported here). nil clears it: on appstore no wasm then loads.
func SetBundledCodeVerifier(v BundledCodeVerifier) {
	bundledMu.Lock()
	bundledVerifier = v
	bundledMu.Unlock()
}

// codeLoadAllowed reports whether wasm, the bytes of plugin id@version read
// from path, may be compiled: always off appstore; on appstore only a
// lock-verified bundled one.
func codeLoadAllowed(id, version, path string, wasm []byte) bool {
	if codeInstallAllowed() {
		return true
	}
	bundledMu.RLock()
	v := bundledVerifier
	bundledMu.RUnlock()
	return v != nil && v(id, version, path, wasm)
}
