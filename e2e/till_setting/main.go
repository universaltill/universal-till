// Command till_setting sets or deletes one setting in a RUNNING e2e till's
// database, out of band (ut-docs#5275, confirm-dialog-escape-focus-5275.spec.ts
// uses it to make the worker till an "additional" till with an unreachable
// main). Test support only, like e2e/category_picture.
//
//	UT_DATA_DIR=<dir> go run ./e2e/till_setting set <key> <value>
//	UT_DATA_DIR=<dir> go run ./e2e/till_setting delete <key>
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/paths"
)

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}

func main() {
	dataDir := os.Getenv("UT_DATA_DIR")
	if dataDir == "" || len(os.Args) < 3 {
		fatalf("usage: UT_DATA_DIR=<dir> till_setting set <key> <value> | delete <key>")
	}
	paths.Init(dataDir)
	conn, err := db.Open(filepath.Join(dataDir, "unitill-pos.db"))
	if err != nil {
		fatalf("open db: %v", err)
	}
	defer conn.Close()
	repo := data.NewSettingsRepo(conn.DB)
	ctx := context.Background()
	switch {
	case os.Args[1] == "set" && len(os.Args) == 4:
		err = repo.Set(ctx, os.Args[2], os.Args[3])
	case os.Args[1] == "delete" && len(os.Args) == 3:
		err = repo.Delete(ctx, os.Args[2])
	default:
		fatalf("usage: till_setting set <key> <value> | delete <key>")
	}
	if err != nil {
		fatalf("%v", err)
	}
}
