package offsitebackup

import (
	"context"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/entitlement"
)

// Store is the settings access the package needs (data.SettingsRepo).
type Store interface {
	Get(ctx context.Context, key string) (string, bool, error)
	Set(ctx context.Context, key, value string) error
	Delete(ctx context.Context, key string) error
	SetMany(ctx context.Context, kv map[string]string) error
}

// GateState says whether this till may upload, and if not, why.
type GateState int

const (
	// GateOpen: a paired main till on a plan with cloud backup.
	GateOpen GateState = iota
	// GateUnpaired: not connected to a my. shop.
	GateUnpaired
	// GateNotMain: an additional till (sync.primary_url set), or one the owner replaced. Only the main
	// till is the shop's fiscal recorder and uploads (ADR-0086, ADR-0199).
	GateNotMain
	// GateNotEntitled: the plan does not include cloud backup (or the cached
	// entitlement is stale: fail closed, ADR-0060).
	GateNotEntitled
)

// CheckGate decides from local state only; it never calls the network.
func CheckGate(ctx context.Context, cfg *config.Config, settings Store, now time.Time) GateState {
	if !enroll.CredentialsComplete(enroll.Effective(cfg).Marketplace) {
		return GateUnpaired
	}
	if v, _, _ := settings.Get(ctx, "sync.primary_url"); strings.TrimSpace(v) != "" {
		return GateNotMain
	}
	if _, replaced := enroll.ReplacedState(ctx, settings); replaced {
		return GateNotMain // ut-docs#5058: the till that replaced it is the main till now
	}
	if !entitlement.Allows(entitlement.EffectivePlan(ctx, settings, now), entitlement.CapCloudBackup) {
		return GateNotEntitled
	}
	return GateOpen
}

// ConnFromConfig resolves the live cloud connection for HTTPCloud.
func ConnFromConfig(cfg *config.Config) func() Conn {
	return func() Conn {
		m := enroll.Effective(cfg).Marketplace
		return Conn{BaseURL: m.EndpointURL, StoreID: m.StoreID, Token: m.MerchantToken}
	}
}
