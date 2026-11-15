package main

import (
	"bytes"
	"crypto/ed25519"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/releasesig"
)

var sigData = []byte("abc  unitill-pos_1.0.0_linux_amd64.tar.gz\n")

func tSeed(n byte) []byte { return bytes.Repeat([]byte{n}, ed25519.SeedSize) }

func tPub(n byte) ed25519.PublicKey {
	return ed25519.NewKeyFromSeed(tSeed(n)).Public().(ed25519.PublicKey)
}

func tKey(n byte) releasesig.Key { return releasesig.Key{ID: releasesig.KeyID(tPub(n)), Pub: tPub(n)} }

func TestAppendKeepsOldLines(t *testing.T) {
	l1, l2 := sign(tSeed(1), sigData), sign(tSeed(2), sigData)
	out := appendLine([]byte(l1), l2)
	if string(out) != l1+"\n"+l2+"\n" {
		t.Fatalf("got %q", out)
	}
	if string(appendLine(nil, l1)) != l1+"\n" {
		t.Fatal("nil existing")
	}
	if _, err := releasesig.Verify([]releasesig.Key{tKey(1), tKey(2)}, sigData, out); err != nil {
		t.Fatal(err)
	}
}

// Re-running macos-dmg-attach must not grow the .sig (ADR-0178): each run
// re-signs a changed checksums.txt, keeping only lines that still cover a
// checksums.txt a till could read.
func TestAppendPrunedIdempotentOverReRuns(t *testing.T) {
	pre := []byte("aaa  x\n")
	sig := appendLine(nil, sign(tSeed(1), pre)) // checksums job
	cur := pre
	for run := 0; run < 12; run++ {
		old := cur
		// each re-run rebuilds the .dmg: a new line content every time
		cur = []byte("aaa  x\nd" + string(rune('a'+run)) + "d  y.dmg\n")
		sig = appendPruned(sig, sign(tSeed(1), cur), tPub(1), old)
		lines := strings.Count(string(sig), "\n")
		if lines > 2 {
			t.Fatalf("run %d: %d lines, want <= 2", run, lines)
		}
		for _, d := range [][]byte{old, cur} {
			if _, err := releasesig.Verify([]releasesig.Key{tKey(1)}, d, sig); err != nil {
				t.Fatalf("run %d: %v", run, err)
			}
		}
	}
	// Re-signing the same bytes does not grow the file.
	n := string(sig)
	again := appendPruned(sig, sign(tSeed(1), cur), tPub(1), cur)
	if c := strings.Count(string(again), "\n"); c != 1 {
		t.Fatalf("same-bytes re-sign left %d lines (%s)", c, n)
	}
	// Another key's line is never dropped.
	other := sign(tSeed(2), pre)
	out := appendPruned([]byte(other+"\n"), sign(tSeed(1), cur), tPub(1), cur)
	if !strings.Contains(string(out), other) {
		t.Fatal("other key's line dropped")
	}
}
