// Config resolution for unitill-uninstall: honour the exact same
// DataDir/DBPath the running service resolves. The .deb's service unit
// reads /opt/unitill/pos.env via EnvironmentFile= AND pins a default
// Environment=UT_DATA_DIR=/opt/unitill/data (packaging/systemd/
// unitill-pos.service) — this CLI is not run under systemd, so it
// reproduces both layers itself, then hands off to the same config.Init
// the service boots through.
package main

import (
	"os"

	"github.com/joho/godotenv"
	"github.com/universaltill/universal-till/internal/config"
)

// debDataDir mirrors unitill-pos.service's Environment=UT_DATA_DIR line —
// the layer UNDER pos.env in systemd's precedence.
const debDataDir = "/opt/unitill/data"

// debPosEnv is the .deb's EnvironmentFile (overridable for dev/tests via
// UT_ENV_FILE, the same env var internal/app.Run already honours).
const debPosEnv = "/opt/unitill/pos.env"

// loadServiceConfig loads envFile (KEY=VALUE lines, '#' comments and blank
// lines skipped, split on the FIRST '=' — godotenv, the same parser
// internal/app.Run uses for UT_ENV_FILE), then applies defaultDataDir the
// way the unit's Environment= line would (only when neither the process
// env nor envFile set UT_DATA_DIR), then runs config.Init. Precedence, top
// wins: process env > envFile > defaultDataDir > config.Init defaults —
// the same order systemd gives the service (modulo process env, which a
// CLI invoker legitimately controls).
//
// A missing envFile is not an error: a hand-built box without pos.env
// still resolves the unit-default data dir.
func loadServiceConfig(envFile, defaultDataDir string) (*config.Config, error) {
	_ = godotenv.Load(envFile) // sets only vars not already in the process env
	if os.Getenv("UT_DATA_DIR") == "" && defaultDataDir != "" {
		if err := os.Setenv("UT_DATA_DIR", defaultDataDir); err != nil {
			return nil, err
		}
	}
	return config.Init()
}
