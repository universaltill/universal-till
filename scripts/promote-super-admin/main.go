// Command promote-super-admin bootstraps the very first super_admin user
// on a till (ut-docs#761). This is the one gap the in-app promotion path
// (Users → "Promote to super admin", gated on the permission_management
// action) can never close on its own: that gate requires an existing
// super_admin, so the first one has to come from somewhere else.
//
// This is deliberately NOT test/seed tooling — unlike scripts/e2e_seed, it's
// a real production bootstrap operation an operator runs once against a
// real till's DB — so it goes through internal/data's AuthRepo/POSRepo
// like any other write, never a raw SQL statement of its own.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

func main() {
	force := flag.Bool("force", false, "promote even if a super_admin already exists (deliberate disaster-recovery re-bootstrap only)")
	flag.Parse()
	args := flag.Args()
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: promote-super-admin [--force] <db-path> <username>")
		os.Exit(2)
	}

	if err := run(args[0], args[1], *force); err != nil {
		fmt.Fprintf(os.Stderr, "promote-super-admin: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("promoted %q to super_admin\n", args[1])
}

// run does the actual work, separated from main so it's directly testable
// (main_test.go), same shape as scripts/ci/checkhelptopics' own split.
func run(dbPath, username string, force bool) error {
	// db.Open MkdirAll's the parent directory and migrates whatever it
	// finds — so a typo'd path would silently create a brand-new, empty,
	// fully-migrated DB instead of failing, and the operator would then
	// see the misleading "no user with username" error rather than "wrong
	// path" (ut-docs#761 review finding 7). Refuse up front instead.
	if _, err := os.Stat(dbPath); err != nil {
		return fmt.Errorf("db path %q: %w (check the path — this tool never creates a new database)", dbPath, err)
	}

	d, err := db.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer d.Close()

	ctx := context.Background()
	repo := data.NewAuthRepo(d.DB)
	posRepo := data.NewPOSRepo(d.DB)

	// Refuse to become a standing backdoor next to the in-app promotion
	// path: once at least one super_admin exists, further promotions
	// should go through that gated, journaled UI flow, not this CLI.
	existing, err := repo.CountUsersByRole(ctx, "super_admin")
	if err != nil {
		return fmt.Errorf("count existing super_admins: %w", err)
	}
	if existing > 0 && !force {
		return fmt.Errorf("a super_admin already exists (%d) — promote further users from Users in the app (as an existing super_admin), or pass --force for a deliberate disaster-recovery re-bootstrap", existing)
	}

	users, err := repo.ListUsers(ctx)
	if err != nil {
		return fmt.Errorf("list users: %w", err)
	}
	var target *data.UserRow
	for i := range users {
		if users[i].Username == username {
			target = &users[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("no user with username %q — create the account first (setup wizard or Users), then promote it", username)
	}
	if target.Role == "super_admin" {
		return fmt.Errorf("%q is already super_admin", username)
	}

	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if err := repo.SetUserRole(ctx, tx, target.ID, "super_admin"); err != nil {
		return fmt.Errorf("set role: %w", err)
	}
	// Actor "system" mirrors the convention InsertAudit's own doc comment
	// and existing call sites use for actions the till itself performs
	// rather than a logged-in operator (e.g. auth_page.go's first-boot
	// setup) — there is no HTTP session here.
	if err := posRepo.InsertAudit(ctx, tx, "system", "user", target.ID, "user_role_changed",
		map[string]any{"from": target.Role, "to": "super_admin", "via": "bootstrap-cli"},
		time.Now().UTC().Format(time.RFC3339), ""); err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}
