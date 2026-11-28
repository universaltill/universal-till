// Command register-audit is the read-only ghost audit for registers
// (ut-docs#7098, epic #7097): it lists every register with the till and device
// it belongs to and says which are old entries that a manager may retire.
//
//	register-audit --db <path> [--cloud-devices <file.json>] [--days 30] [--format table|json]
//
// It opens the database with mode=ro and query_only(1), runs no migration and
// writes nothing. Run it on a COPY of the till's database. The optional cloud
// device list is a local JSON file the operator saved separately; this tool
// makes no network call and never prints a key, token or bearer hash.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"time"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/registeraudit"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func buildInfo() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, s := range bi.Settings {
		if s.Key == "vcs.revision" && len(s.Value) >= 8 {
			return s.Value[:8]
		}
	}
	return bi.Main.Version
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("register-audit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "", "path to the till's SQLite database (use a copy)")
	cloudPath := fs.String("cloud-devices", "", "optional JSON file with the account's device list")
	days := fs.Int("days", registeraudit.DefaultQuietDays, "days without a sale before a register counts as quiet")
	format := fs.String("format", "table", "table or json")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dbPath == "" || (*format != "table" && *format != "json") {
		fmt.Fprintln(stderr, "usage: register-audit --db <path> [--cloud-devices <file.json>] [--days 30] [--format table|json]")
		return 2
	}
	d, err := db.OpenAuditReadOnly(*dbPath)
	if err != nil {
		fmt.Fprintln(stderr, "register-audit:", err)
		return 1
	}
	defer d.Close()
	opts := registeraudit.Options{Now: time.Now(), QuietDays: *days, Build: buildInfo()}
	if *cloudPath != "" {
		devs, err := registeraudit.LoadCloudDevices(*cloudPath)
		if err != nil {
			fmt.Fprintln(stderr, "register-audit:", err)
			return 1
		}
		if devs == nil {
			devs = []registeraudit.CloudDevice{}
		}
		opts.Cloud = devs
	}
	rep, err := registeraudit.Build(context.Background(), d.DB, opts)
	if err != nil {
		fmt.Fprintln(stderr, "register-audit:", err)
		return 1
	}
	if *format == "json" {
		err = registeraudit.WriteJSON(stdout, rep)
	} else {
		err = registeraudit.WriteTable(stdout, rep)
	}
	if err != nil {
		fmt.Fprintln(stderr, "register-audit:", err)
		return 1
	}
	return 0
}
