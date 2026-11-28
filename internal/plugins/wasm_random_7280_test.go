package plugins

import "testing"

// ut-docs#7280: WASI random_get must be seeded from the host's crypto/rand.
// wazero's default is a deterministic source, so every fresh instance drew
// the same bytes and ids/tokens/nonces generated in a guest collided.
func TestWasmGuestRandomIsNotDeterministic(t *testing.T) {
	guest := buildHostfnGuest(t)
	d := hostfnTestDB(t)
	const pluginID = "com.test.random"
	seedPlugin(t, d, pluginID)
	grantPerm(t, d, pluginID, "storage")

	w := NewWasmRuntime(t.TempDir())
	if err := w.load(pluginID, "1.0.0", guest); err != nil {
		t.Fatalf("load: %v", err)
	}
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		res := runGuestPayload(t, w, d, pluginID, map[string]string{"mode": "random"})
		got, _ := res["random_hex"].(string)
		if len(got) != 32 {
			t.Fatalf("instance %d: random_hex = %q, want 32 hex chars", i, got)
		}
		if seen[got] {
			t.Fatalf("instance %d drew random bytes %s that an earlier fresh instance already drew: random_get is deterministic", i, got)
		}
		seen[got] = true
	}
}
