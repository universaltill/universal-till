// Command seed_release_notes makes a throwaway e2e till look like an
// existing shop that has just been updated (ut-docs#3091): setup is complete
// and the till last ran an older version. On boot the till then records the
// new version and — because it is not a fresh install — shows the one-time
// "Updated to … — see what's new" chip that release-notes-3091.spec.ts drives.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

func main() {
	dataDir := os.Getenv("UT_DATA_DIR")
	if dataDir == "" {
		fmt.Fprintln(os.Stderr, "UT_DATA_DIR must be set")
		os.Exit(2)
	}
	conn, err := db.Open(filepath.Join(dataDir, "unitill-pos.db"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		os.Exit(2)
	}
	defer conn.Close()
	repo := data.NewSettingsRepo(conn.DB)
	ctx := context.Background()
	for k, v := range map[string]string{
		"setup.completed":          "true",
		data.AppVersionSettingsKey: "0.0.1",
	} {
		if err := repo.Set(ctx, k, v); err != nil {
			fmt.Fprintf(os.Stderr, "set %s: %v\n", k, err)
			os.Exit(2)
		}
	}
	fmt.Println("till seeded as updated from v0.0.1")
}
