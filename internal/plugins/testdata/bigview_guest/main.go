//go:build wasip1

// Test guest for ut-docs#3160: answers any event with a 2 MiB JSON
// document on stdout, over the 1 MiB cap the host puts on a ui.* answer.
package main

import (
	"fmt"
	"strings"
)

func main() {
	fmt.Printf(`{"document":{"version":1,"components":[{"type":"text","text":{"literal":"%s"}}]}}`+"\n", strings.Repeat("A", 2<<20))
}
