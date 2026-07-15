//go:build !desktop

// Package main (unitill-desktop) is a native webview shell around the POS
// server. The real implementation is CGO (system WebView) and lives behind the
// `desktop` build tag; this stub keeps `go build ./...` / `go test ./...` (and
// CI, which never sets the tag) compiling without a WebView toolchain.
package main

import "fmt"

func main() {
	fmt.Println("unitill-desktop is the native desktop shell.")
	fmt.Println("Build it with:  go build -tags desktop ./cmd/unitill-desktop")
}
