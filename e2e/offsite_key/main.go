// Command offsite_key gives an e2e till's data dir a cloud-backup key for one
// store id, so the Backups card asks the "key in the cloud" question
// (ut-docs#7171, keychoice-confirm-7171.spec.ts). Test support only, like
// e2e/till_setting.
//
//	UT_DATA_DIR=<dir> go run ./e2e/offsite_key <store id>
package main

import (
	"fmt"
	"os"

	"github.com/universaltill/universal-till/internal/offsitebackup"
	"github.com/universaltill/universal-till/internal/paths"
)

func main() {
	dataDir := os.Getenv("UT_DATA_DIR")
	if dataDir == "" || len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: UT_DATA_DIR=<dir> offsite_key <store id>")
		os.Exit(2)
	}
	paths.Init(dataDir)
	if _, _, err := (offsitebackup.KeyStore{}).Generate(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}
