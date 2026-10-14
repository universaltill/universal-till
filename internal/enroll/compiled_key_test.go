package enroll

import (
	"context"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/buildinfo"
)

// ADR-0158: a key compiled into the binary is never replaced by the
// persisted/fetched key, and no fetch happens while one exists — even for a
// config that did not come through config.Init (so its PublicKey is empty).
func TestCompiledKeyNeverReplacedOrFetched(t *testing.T) {
	resetState()
	srv, _ := testMarketplace(t, 0)
	kv := newFakeKV()
	compiled := strings.Repeat("12", 32)
	orig := buildinfo.MarketplacePublicKey
	buildinfo.MarketplacePublicKey = compiled
	t.Cleanup(func() { buildinfo.MarketplacePublicKey = orig })

	// A different key persisted in the settings table (e.g. a tampered row).
	mu.Lock()
	cur.PublicKey = strings.Repeat("ef", 32)
	mu.Unlock()
	t.Cleanup(resetState)

	cfg := freshConfig(srv.URL) // PublicKey empty
	if got := Effective(cfg).Marketplace.PublicKey; got != compiled {
		t.Fatalf("Effective key = %q, want the compiled key", got)
	}
	if got := EnsureSigningKey(context.Background(), cfg, kv).Marketplace.PublicKey; got != compiled {
		t.Fatalf("EnsureSigningKey key = %q, want the compiled key", got)
	}

	// No persisted key either: still no fetch.
	resetState()
	if got := EnsureSigningKey(context.Background(), freshConfig(srv.URL), kv).Marketplace.PublicKey; got != compiled {
		t.Fatalf("EnsureSigningKey key = %q, want the compiled key", got)
	}
	if n := signingKeyHits.Load(); n != 0 {
		t.Fatalf("signing-key requests = %d with a compiled key, want 0", n)
	}
}
