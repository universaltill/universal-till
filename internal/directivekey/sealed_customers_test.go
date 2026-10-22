package directivekey

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/universaltill/universal-till/internal/hpke"
)

// sealTo seals pt to the store's key the way the cloud's payload seal does
// (ADR-0168 §5): same wire form as pin_sealed, a different info and AAD.
func sealTo(t *testing.T, s *Store, info, aad string, pt []byte) string {
	t.Helper()
	k, err := s.LoadOrCreate()
	if err != nil {
		t.Fatal(err)
	}
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	enc, ct, err := hpke.SealDeterministic(k.PublicKey(), eph, []byte(info), []byte(aad), pt)
	if err != nil {
		t.Fatal(err)
	}
	return "v1." + KID(k.PublicKey().Bytes()) + "." + base64.RawURLEncoding.EncodeToString(enc) + "." + base64.RawURLEncoding.EncodeToString(ct)
}

func TestCustomersAAD(t *testing.T) {
	got := CustomersAAD("store-1", "dir-1", "import_customers", "job-1", 3)
	if want := "store-1\ndir-1\nimport_customers\njob-1\n3"; got != want {
		t.Fatalf("aad = %q, want %q", got, want)
	}
}

func TestOpenSealed_CustomersRoundTripAndBinding(t *testing.T) {
	s := NewAt(t.TempDir() + "/secrets/directive-x25519.key")
	aad := CustomersAAD("store-1", "dir-1", "import_customers", "job-1", 0)
	sealed := sealTo(t, s, CustomersInfo, aad, []byte(`{"rows":[]}`))

	pt, err := s.OpenSealed(sealed, CustomersInfo, aad)
	if err != nil || string(pt) != `{"rows":[]}` {
		t.Fatalf("OpenSealed = %q, %v", pt, err)
	}
	// Bound to its directive, job and chunk, and to its purpose.
	for name, c := range map[string]struct{ info, aad string }{
		"other chunk":     {CustomersInfo, CustomersAAD("store-1", "dir-1", "import_customers", "job-1", 1)},
		"other directive": {CustomersInfo, CustomersAAD("store-1", "dir-2", "import_customers", "job-1", 0)},
		"other store":     {CustomersInfo, CustomersAAD("store-2", "dir-1", "import_customers", "job-1", 0)},
		"pin info":        {Info, aad},
	} {
		if _, err := s.OpenSealed(sealed, c.info, c.aad); !errors.Is(err, ErrKeyGone) {
			t.Errorf("%s: err = %v, want ErrKeyGone", name, err)
		}
	}
	// A PIN seal never opens as a customer payload, nor the reverse.
	pin := sealTo(t, s, Info, AAD("store-1", "d", "set_user_pin", "u"), []byte("1234"))
	if _, err := s.OpenSealed(pin, CustomersInfo, AAD("store-1", "d", "set_user_pin", "u")); err == nil {
		t.Fatal("a PIN seal opened as a customer payload")
	}
	if _, err := s.OpenPIN(sealed, aad); err == nil {
		t.Fatal("a customer seal opened as a PIN")
	}
	if _, err := s.OpenSealed("garbage", CustomersInfo, aad); !errors.Is(err, ErrMalformed) {
		t.Fatalf("garbage: %v", err)
	}
}

func TestOpenSealed_NoKeyMeansKeyGone(t *testing.T) {
	s := NewAt(t.TempDir() + "/secrets/directive-x25519.key")
	other := NewAt(t.TempDir() + "/secrets/directive-x25519.key")
	aad := CustomersAAD("s", "d", "import_customers", "j", 0)
	sealed := sealTo(t, other, CustomersInfo, aad, []byte("x"))
	if _, err := s.OpenSealed(sealed, CustomersInfo, aad); !errors.Is(err, ErrKeyGone) {
		t.Fatalf("no key: %v", err)
	}
}
