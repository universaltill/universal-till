// Command printsource prints the release source compiled into it; the ldflags
// test builds it with -X to pin the buildinfo symbol path.
package main

import (
	"fmt"
	"os"

	"github.com/universaltill/universal-till/internal/releasesource"
)

func main() {
	s, err := releasesource.Build()
	if err != nil {
		fmt.Println("ERR", err)
		os.Exit(1)
	}
	fmt.Println(s.LatestURL())
}
