// Command releasesig signs and verifies a till release's checksums.txt in CI
// (ADR-0178), verifying with internal/releasesig so CI and the till agree byte for byte.
//
//	go run ./scripts/releasesig keygen
//	go run ./scripts/releasesig sign   --checksums F [--existing-sig S] --out O
//	go run ./scripts/releasesig verify --checksums F --sig S
//
// sign reads the Ed25519 seed (64 hex) from UT_RELEASE_SIGNING_KEY and the
// trusted public keys from UT_RELEASE_KEYS_HEX; it refuses a seed whose key
// is not in that list. verify reads UT_RELEASE_KEYS_HEX. Neither ever prints
// the seed (keygen prints a fresh one by design: run it offline).
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/universaltill/universal-till/internal/releasesig"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Getenv); err != nil {
		fmt.Fprintf(os.Stderr, "::error::releasesig: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer, env func(string) string) error {
	if len(args) == 0 {
		return errors.New("usage: releasesig keygen|sign|verify")
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	checksums := fs.String("checksums", "", "checksums.txt path")
	existing := fs.String("existing-sig", "", "existing checksums.txt.sig to append to (optional)")
	keep := fs.String("keep-checksums", "", "with --existing-sig: the previously published checksums.txt whose signature line must survive; other lines by this key are dropped so re-runs stay bounded")
	outPath := fs.String("out", "", "output path")
	sigPath := fs.String("sig", "", "checksums.txt.sig path")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	switch args[0] {
	case "keygen":
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "seed   %s\npublic %s\nkey_id %s\n", hex.EncodeToString(priv.Seed()), hex.EncodeToString(pub), releasesig.KeyID(pub))
		return nil
	case "sign":
		if *checksums == "" || *outPath == "" {
			return errors.New("sign needs --checksums and --out")
		}
		keys, err := keysFromEnv(env)
		if err != nil {
			return err
		}
		seed, err := hex.DecodeString(env("UT_RELEASE_SIGNING_KEY"))
		if err != nil || len(seed) != ed25519.SeedSize {
			return errors.New("UT_RELEASE_SIGNING_KEY must be 64 hex characters") // never echo the value
		}
		pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
		trusted := false
		for _, k := range keys {
			if k.ID == releasesig.KeyID(pub) {
				trusted = true
			}
		}
		if !trusted {
			return fmt.Errorf("the signing key (id %s) is not in UT_RELEASE_KEYS_HEX: a build stamped with those keys would refuse this release", releasesig.KeyID(pub))
		}
		data, err := os.ReadFile(*checksums)
		if err != nil {
			return err
		}
		var old []byte
		if *existing != "" {
			if old, err = os.ReadFile(*existing); err != nil {
				return err
			}
		}
		line := sign(seed, data)
		if *keep == "" {
			return os.WriteFile(*outPath, appendLine(old, line), 0o644)
		}
		prev, err := os.ReadFile(*keep)
		if err != nil {
			return err
		}
		return os.WriteFile(*outPath, appendPruned(old, line, pub, prev), 0o644)
	case "verify":
		if *checksums == "" || *sigPath == "" {
			return errors.New("verify needs --checksums and --sig")
		}
		keys, err := keysFromEnv(env)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(*checksums)
		if err != nil {
			return err
		}
		sig, err := os.ReadFile(*sigPath)
		if err != nil {
			return err
		}
		id, err := releasesig.Verify(keys, data, sig)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "verified %s with key %s\n", *checksums, id)
		return nil
	}
	return fmt.Errorf("unknown command %q", args[0])
}

func keysFromEnv(env func(string) string) ([]releasesig.Key, error) {
	keys, err := releasesig.ParseKeys(env("UT_RELEASE_KEYS_HEX"))
	if err != nil {
		return nil, fmt.Errorf("UT_RELEASE_KEYS_HEX: %v", err)
	}
	if len(keys) == 0 {
		return nil, errors.New("UT_RELEASE_KEYS_HEX is empty")
	}
	return keys, nil
}
