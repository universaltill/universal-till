package pages

import (
	"context"
	"runtime"
	"strings"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/logging"
)

// settingsGetter is the one read TillStartupInfo needs from the settings
// store (satisfied by *settings.Store and *data.SettingsRepo).
type settingsGetter interface {
	Get(ctx context.Context, key string) (string, bool, error)
}

// tillRole mirrors cloudsync's heartbeat role rule: a till is the primary
// unless sync.primary_url points at another till; back-office display mode
// wins over both.
func tillRole(ctx context.Context, kv settingsGetter) string {
	get := func(k string) string {
		v, _, _ := kv.Get(ctx, k)
		return strings.TrimSpace(v)
	}
	role := "primary"
	if get("sync.primary_url") != "" {
		role = "replica"
	}
	if get("display.mode") == "backoffice" {
		role = "backoffice"
	}
	return role
}

// TillStartupInfo gathers the facts for the startup log line and the
// Settings diagnostics summary (ut-docs#2720). envFile is the pos.env path
// actually loaded ("" = none). Call after enroll.Init so the enrolment
// state and the effective cloud endpoint are settled.
func TillStartupInfo(ctx context.Context, cfg *config.Config, kv settingsGetter, envFile string) logging.StartupInfo {
	eff := enroll.Effective(cfg)
	st := enroll.CurrentStatus()
	return logging.StartupInfo{
		Version:     buildinfo.Version,
		GOOS:        runtime.GOOS,
		GOARCH:      runtime.GOARCH,
		DataDir:     cfg.DataDir,
		EnvFile:     envFile,
		EndpointURL: eff.Marketplace.EndpointURL,
		Enrolled:    st.Registered,
		StoreID:     st.StoreID,
		Role:        tillRole(ctx, kv),
		LogFile:     logging.FilePath(),
	}
}
