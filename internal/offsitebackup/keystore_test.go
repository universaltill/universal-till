package offsitebackup

import (
	"bytes"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestHKDFKnownAnswer(t *testing.T) {
	raw, err := os.ReadFile("testdata/hkdf_v1.txt")
	if err != nil {
		t.Fatal(err)
	}
	kv := map[string]string{}
	for _, l := range strings.Split(string(raw), "\n") {
		if l = strings.TrimSpace(l); l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		k, v, _ := strings.Cut(l, "=")
		kv[k] = v
	}
	dek, _ := hex.DecodeString(kv["dek_hex"])
	bid, _ := parseUUID(kv["backup_id_hex"])
	got, err := DeriveKey(dek, bid, kv["store_id"])
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(got) != kv["derived_key_hex"] {
		t.Fatalf("derived %x, want %s", got, kv["derived_key_hex"])
	}
	if Fingerprint(dek) != kv["fingerprint"] {
		t.Fatalf("fingerprint %s, want %s", Fingerprint(dek), kv["fingerprint"])
	}
	// A different store or backup id gives a different key.
	other, _ := DeriveKey(dek, bid, "st_other")
	if bytes.Equal(got, other) {
		t.Error("store id not bound into the key")
	}
}

func TestKeyStoreSaveLoadModesAndStoreBinding(t *testing.T) {
	ks := KeyStore{Dir: filepath.Join(t.TempDir(), "keys")}
	if _, _, err := ks.Load("st_1"); !errors.Is(err, ErrNoKey) {
		t.Fatalf("empty store: %v", err)
	}
	dek, gen, err := ks.Generate("st_1")
	if err != nil || len(dek) != KeySize || gen != 1 {
		t.Fatalf("generate: %v %d %v", len(dek), gen, err)
	}
	if runtime.GOOS != "windows" {
		di, _ := os.Stat(ks.Dir)
		fi, _ := os.Stat(ks.path())
		if di.Mode().Perm() != 0o700 || fi.Mode().Perm() != 0o600 {
			t.Errorf("modes dir=%v file=%v", di.Mode().Perm(), fi.Mode().Perm())
		}
	}
	got, g2, err := ks.Load("st_1")
	if err != nil || !bytes.Equal(got, dek) || g2 != 1 {
		t.Fatalf("load: %v", err)
	}
	// No temp files left behind by the atomic write.
	es, _ := os.ReadDir(ks.Dir)
	if len(es) != 1 {
		t.Errorf("dir holds %d entries", len(es))
	}
	// A key of another store is not this store's key, but a READ never
	// deletes it (H1): neither another id nor a transient "" store id.
	if _, _, err := ks.Load("st_2"); !errors.Is(err, ErrNoKey) {
		t.Fatalf("other store: %v", err)
	}
	if _, _, err := ks.Load(""); !errors.Is(err, ErrNoKey) {
		t.Fatalf("empty store id: %v", err)
	}
	if ks.Older("") != nil {
		t.Error("Older with an empty store id returned keys")
	}
	if got, _, err := ks.Load("st_1"); err != nil || !bytes.Equal(got, dek) {
		t.Fatalf("a read removed or changed the key file: %v", err)
	}
	// Generate never overwrites an existing key.
	_, _, _ = ks.Generate("st_1")
	if _, _, err := ks.Generate("st_1"); err == nil {
		t.Error("generate overwrote a key")
	}
	ks.Delete()
	ks.Delete() // idempotent
	if _, _, err := ks.Load("st_1"); !errors.Is(err, ErrNoKey) {
		t.Fatal("not deleted")
	}
}

func TestKeyStoreRejectsCorruptFile(t *testing.T) {
	ks := KeyStore{Dir: t.TempDir()}
	_ = os.WriteFile(ks.path(), []byte(`{"v":1,"store_id":"st_1","generation":1,"dek_b64":"AAAA"}`), 0o600)
	if _, _, err := ks.Load("st_1"); !errors.Is(err, ErrNoKey) {
		t.Fatalf("short key accepted: %v", err)
	}
}

func TestDeleteForStoreOnlyRemovesThatShopsKey(t *testing.T) {
	ks := KeyStore{Dir: t.TempDir()}
	dek, _, _ := ks.Generate("st_1")
	ks.DeleteForStore("")
	ks.DeleteForStore("st_other")
	if got, _, err := ks.Load("st_1"); err != nil || !bytes.Equal(got, dek) {
		t.Fatalf("key lost by a delete for another shop: %v", err)
	}
	ks.DeleteForStore("st_1")
	if _, err := os.Stat(ks.path()); !os.IsNotExist(err) {
		t.Fatal("the left shop's key was kept")
	}
}
