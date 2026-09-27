package directivekey

import (
	"bytes"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/hpke"
	"github.com/universaltill/universal-till/internal/logging"
)

// sharedVector is the cross-repo PIN seal vector. The canonical copy lives
// in ut-docs at reference/contracts/till-user-pin-v1.json; testdata holds a
// byte-identical copy (ut-cloud's sealer test reads the same values).
type sharedVector struct {
	RecipientSK     string `json:"recipient_sk"`
	RecipientPK     string `json:"recipient_pk"`
	PublicKeyB64URL string `json:"public_key_b64url"`
	KID             string `json:"kid"`
	StoreExternalID string `json:"store_external_id"`
	DirectiveID     string `json:"directive_id"`
	Type            string `json:"type"`
	UserID          string `json:"user_id"`
	PIN             string `json:"pin"`
	Info            string `json:"info"`
	AAD             string `json:"aad"`
	SharedSecret    string `json:"shared_secret"`
	Key             string `json:"key"`
	BaseNonce       string `json:"base_nonce"`
	Enc             string `json:"enc"`
	Ciphertext      string `json:"ciphertext"`
	PINSealed       string `json:"pin_sealed"`
}

func loadVector(t *testing.T) sharedVector {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "till-user-pin-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v sharedVector
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// storeWithKey writes skHex as the key file of a fresh store.
func storeWithKey(t *testing.T, skHex string) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secrets", "directive-x25519.key")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	sk, err := hex.DecodeString(skHex)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, sk, 0o600); err != nil {
		t.Fatal(err)
	}
	return NewAt(path)
}

func TestSharedVector_Constants(t *testing.T) {
	v := loadVector(t)
	if v.Info != Info {
		t.Fatalf("info = %q, want %q", v.Info, Info)
	}
	if got := AAD(v.StoreExternalID, v.DirectiveID, v.Type, v.UserID); got != v.AAD {
		t.Fatalf("aad = %q, want %q", got, v.AAD)
	}
}

// The till's key schedule, step by step against the vector: a mismatch
// names the first diverging value (shared_secret -> key -> base_nonce ->
// enc -> ciphertext), so a divergence from ut-cloud's sealer shows where.
func TestSharedVector_Intermediates(t *testing.T) {
	v := loadVector(t)
	sk, _ := hex.DecodeString(v.RecipientSK)
	skR, err := ecdh.X25519().NewPrivateKey(sk)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(v.PINSealed, ".")
	if len(parts) != 4 || parts[0] != "v1" || parts[1] != v.KID {
		t.Fatalf("pin_sealed framing: %q", v.PINSealed)
	}
	enc, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	ct, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		t.Fatal(err)
	}
	s, err := hpke.SetupBaseR(skR, enc, []byte(v.Info))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, got, want string }{
		{"shared_secret", hex.EncodeToString(s.SharedSecret), v.SharedSecret},
		{"key", hex.EncodeToString(s.Key), v.Key},
		{"base_nonce", hex.EncodeToString(s.BaseNonce), v.BaseNonce},
		{"enc", hex.EncodeToString(enc), v.Enc},
		{"ciphertext", hex.EncodeToString(ct), v.Ciphertext},
	} {
		if c.got != c.want {
			t.Fatalf("first diverging value: %s = %s, want %s", c.name, c.got, c.want)
		}
	}
}

func TestSharedVector_OpenPIN(t *testing.T) {
	v := loadVector(t)
	st := storeWithKey(t, v.RecipientSK)
	rep := st.Report()
	if rep == nil {
		t.Fatal("no report for a valid key")
	}
	if rep["kid"] != v.KID || rep["public_key"] != v.PublicKeyB64URL {
		t.Fatalf("report = %v, want kid %s public_key %s", rep, v.KID, v.PublicKeyB64URL)
	}
	pin, err := st.OpenPIN(v.PINSealed, v.AAD)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if pin != v.PIN {
		t.Fatalf("pin = %q, want %q", pin, v.PIN)
	}
}

func TestOpenPIN_WrongKidOrAADOrKeyIsKeyGone(t *testing.T) {
	v := loadVector(t)
	st := storeWithKey(t, v.RecipientSK)
	parts := strings.Split(v.PINSealed, ".")
	wrongKid := strings.Join([]string{"v1", "0000000000000000", parts[2], parts[3]}, ".")
	for name, f := range map[string]func() error{
		"wrong kid": func() error { _, err := st.OpenPIN(wrongKid, v.AAD); return err },
		"wrong aad": func() error {
			_, err := st.OpenPIN(v.PINSealed, AAD(v.StoreExternalID, v.DirectiveID, "save_user", v.UserID))
			return err
		},
		"other directive id": func() error {
			_, err := st.OpenPIN(v.PINSealed, AAD(v.StoreExternalID, "other", v.Type, v.UserID))
			return err
		},
		"no key file": func() error {
			_, err := NewAt(filepath.Join(t.TempDir(), "secrets", "directive-x25519.key")).OpenPIN(v.PINSealed, v.AAD)
			return err
		},
		"other till key": func() error {
			other := storeWithKey(t, strings.Repeat("11", 32))
			// Same kid forged onto another till: the AEAD still refuses.
			_, err := other.OpenPIN(v.PINSealed, v.AAD)
			return err
		},
	} {
		err := f()
		if !errors.Is(err, ErrKeyGone) {
			t.Errorf("%s: err = %v, want ErrKeyGone", name, err)
			continue
		}
		if err.Error() != "This PIN was sent to a till key that no longer exists. Set the PIN again." {
			t.Errorf("%s: sentence = %q", name, err.Error())
		}
	}
}

func TestOpenPIN_Malformed(t *testing.T) {
	v := loadVector(t)
	st := storeWithKey(t, v.RecipientSK)
	parts := strings.Split(v.PINSealed, ".")
	for _, s := range []string{
		"", "v1", "v2." + strings.Join(parts[1:], "."),
		"v1." + v.KID + ".!!!." + parts[3],
		"v1." + v.KID + "." + parts[3] + "." + parts[3], // enc of the wrong length
		v.PINSealed + ".extra",
	} {
		_, err := st.OpenPIN(s, v.AAD)
		if err == nil || !errors.Is(err, ErrMalformed) {
			t.Errorf("%q: err = %v, want ErrMalformed", s, err)
		}
	}
}

func TestOpenPIN_NeverCreatesKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets", "directive-x25519.key")
	v := loadVector(t)
	if _, err := NewAt(path).OpenPIN(v.PINSealed, v.AAD); !errors.Is(err, ErrKeyGone) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("OpenPIN created a key file: %v", err)
	}
}

func TestLoadOrCreate_PermsAtomicStable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secrets")
	path := filepath.Join(dir, "directive-x25519.key")
	st := NewAt(path)
	k1, err := st.LoadOrCreate()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) != 32 || !bytes.Equal(raw, k1.Bytes()) {
		t.Fatalf("key file: %d bytes, %v", len(raw), err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("temp file left behind")
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
			t.Fatalf("dir mode = %o, want 700", fi.Mode().Perm())
		}
		if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
			t.Fatalf("file mode = %o, want 600", fi.Mode().Perm())
		}
	}
	k2, err := NewAt(path).LoadOrCreate()
	if err != nil || !k1.Equal(k2) {
		t.Fatalf("second load differs: %v", err)
	}
	rep := st.Report()
	if rep["kid"] != KID(k1.PublicKey().Bytes()) || len(rep["kid"]) != 16 {
		t.Fatalf("report kid = %q", rep["kid"])
	}
	pub, err := base64.RawURLEncoding.DecodeString(rep["public_key"])
	if err != nil || !bytes.Equal(pub, k1.PublicKey().Bytes()) {
		t.Fatalf("report public_key = %q", rep["public_key"])
	}
}

// Concurrent first check-ins must agree on one key.
func TestLoadOrCreate_Concurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets", "directive-x25519.key")
	st := NewAt(path)
	var wg sync.WaitGroup
	keys := make([]*ecdh.PrivateKey, 8)
	for i := range keys {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			k, err := st.LoadOrCreate()
			if err != nil {
				t.Error(err)
			}
			keys[i] = k
		}(i)
	}
	wg.Wait()
	for _, k := range keys[1:] {
		if !k.Equal(keys[0]) {
			t.Fatal("two keys created")
		}
	}
}

func TestUnparseableKeyNeverOverwritten(t *testing.T) {
	for name, content := range map[string][]byte{
		"short":  []byte("SECRETBYTES-not-a-key"),
		"long":   bytes.Repeat([]byte{0xAB}, 64),
		"empty":  {},
		"33 raw": bytes.Repeat([]byte{0x42}, 33),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "secrets", "directive-x25519.key")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, content, 0o600); err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			var mu sync.Mutex
			restore := logging.CaptureForTest(lockedWriter{&mu, &logs})
			defer restore()

			st := NewAt(path)
			if _, err := st.LoadOrCreate(); err == nil {
				t.Fatal("LoadOrCreate accepted an unparseable file")
			}
			if rep := st.Report(); rep != nil {
				t.Fatalf("reported %v for an unparseable key", rep)
			}
			v := loadVector(t)
			if _, err := st.OpenPIN(v.PINSealed, v.AAD); !errors.Is(err, ErrKeyGone) {
				t.Fatalf("open: %v", err)
			}
			got, _ := os.ReadFile(path)
			if !bytes.Equal(got, content) {
				t.Fatal("the unparseable key file was overwritten")
			}
			mu.Lock()
			out := logs.String()
			mu.Unlock()
			if !strings.Contains(out, "directive key") {
				t.Fatalf("not logged: %q", out)
			}
			if len(content) > 0 && (strings.Contains(out, string(content)) || strings.Contains(out, hex.EncodeToString(content))) {
				t.Fatalf("log carries the key file's bytes: %q", out)
			}
		})
	}
}

func TestKID(t *testing.T) {
	v := loadVector(t)
	pk, _ := hex.DecodeString(v.RecipientPK)
	if got := KID(pk); got != v.KID {
		t.Fatalf("kid = %s, want %s", got, v.KID)
	}
	// Of the raw bytes, not of the base64 string.
	if KID([]byte(v.PublicKeyB64URL)) == v.KID {
		t.Fatal("kid hashed the base64 form")
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	b  *bytes.Buffer
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}
