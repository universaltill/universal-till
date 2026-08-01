//go:build wasip1

// Test guest for ut-docs#202's regression coverage. Answers ANY ".ask"
// event with a large content_b64 field (5000 raw bytes, base64-encoded),
// standing in for a real export.requested.ask response — this guest exists
// so the redaction fix can be proven against a REAL compiled WASM module
// through the real wazero runtime, not just against the pure Go helper
// function in isolation.
package main

import (
	"encoding/base64"
	"fmt"
	"strings"
)

func main() {
	content := []byte(strings.Repeat("A", 5000))
	fmt.Printf(`{"ok":true,"content_b64":"%s"}`+"\n", base64.StdEncoding.EncodeToString(content))
}
