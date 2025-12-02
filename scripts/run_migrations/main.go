package main

import (
	"fmt"
	"os"

	"github.com/universaltill/universal-till/internal/db"
)

func main() {
	path := os.Getenv("UT_DB_PATH")
	if path == "" {
		if len(os.Args) > 1 {
			path = os.Args[1]
		} else {
			path = "./data/unitill-pos.db"
		}
	}

	fmt.Printf("Opening DB and running migrations on %s\n", path)
	d, err := db.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "migration error: %v\n", err)
		os.Exit(2)
	}
	if err := d.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "close error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Migrations applied successfully")
}
