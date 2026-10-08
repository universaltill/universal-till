//go:build wasip1

// Command guest: a WASI command answering one event per instantiation, the
// shape every shipped plugin has today (architecture/wasm-runtime.md). It does
// the same JSON round trip as the reactor guest so the two are comparable.
package main

import (
	"encoding/json"
	"io"
	"os"

	"github.com/universaltill/universal-till/tools/wasmbench/guests/answer"
)

func main() {
	raw, _ := io.ReadAll(os.Stdin)
	out, ok := answer.Answer(raw)
	if !ok {
		os.Exit(2)
	}
	_ = json.NewEncoder(os.Stdout).Encode(json.RawMessage(out))
}
