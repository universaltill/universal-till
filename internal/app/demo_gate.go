package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/paths"
)

// checkDemoGate is the public-demo start gate (ADR-0113 §1.2,
// ut-docs#2687), kept pure so every combination is unit-tested.
//
//   - Demo on (UT_DEMO) needs ALL of: a non-empty UT_DEMO_TOKEN, the
//     paths.Data("demo") marker file the demo broker writes, and the
//     demo_instance flag baked into the template database. Any missing →
//     an error naming each missing piece; the till refuses to start. Auth
//     must also be on: Demo with UT_AUTH=off (cfg.AuthDisabled) refuses
//     too (ADR-0113 §1.9).
//   - Demo off on a database carrying the flag → refuse too, so a demo
//     database is never run as a normal, unrestricted till.
//   - Demo off, no flag (every real till) → nil; nothing changes.
func checkDemoGate(cfg *config.Config, markerPresent, dbFlag bool) error {
	if !cfg.Demo {
		if dbFlag {
			return errors.New("this database is a demo database (demo_instance flag set) but UT_DEMO is off — refusing to start (ADR-0113)")
		}
		return nil
	}
	var missing []string
	if cfg.DemoToken == "" {
		missing = append(missing, "UT_DEMO_TOKEN is empty")
	}
	if cfg.AuthDisabled {
		// ADR-0113 §1.9: a demo till never serves without sign-in.
		missing = append(missing, "UT_AUTH=off (authentication must stay on in demo mode)")
	}
	if !markerPresent {
		missing = append(missing, "the demo marker file "+paths.Data("demo")+" is missing")
	}
	if !dbFlag {
		missing = append(missing, "the database has no demo_instance flag")
	}
	if len(missing) > 0 {
		return fmt.Errorf("UT_DEMO is on but %s — refusing to start in demo mode (ADR-0113)", strings.Join(missing, ", "))
	}
	return nil
}

// demoMarkerPresent reports whether p is a regular file. Lstat, so a
// symlink or a directory named "demo" never counts as the broker's marker.
func demoMarkerPresent(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode().IsRegular()
}

// enforceDemoGate gathers the gate's inputs and applies checkDemoGate. A
// failure to read the database flag is itself a refusal (fail closed).
// Called by Run after the database is open and migrated, before anything
// serves; its error is returned from Run directly, never passed to
// recovery.Classify, so it can never become a recovery-mode server.
func enforceDemoGate(ctx context.Context, cfg *config.Config, database *sql.DB) error {
	flag, err := data.NewDemoInstanceRepo(database).IsDemoInstance(ctx)
	if err != nil {
		return fmt.Errorf("read demo_instance flag: %w — refusing to start (ADR-0113)", err)
	}
	return checkDemoGate(cfg, demoMarkerPresent(paths.Data("demo")), flag)
}
