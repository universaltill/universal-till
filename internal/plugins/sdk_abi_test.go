package plugins

import (
	"testing"

	"github.com/universaltill/universal-till/sdk/plugin"
)

// The guest SDK's ABI constant is the newest wasm_abi this host speaks
// (ADR-0121 F4): bumping one without the other fails here.
func TestGuestSDKABIMatchesHost(t *testing.T) {
	if plugin.ABI != MaxSupportedWasmABI {
		t.Fatalf("sdk/plugin.ABI = %d, host MaxSupportedWasmABI = %d", plugin.ABI, MaxSupportedWasmABI)
	}
}
