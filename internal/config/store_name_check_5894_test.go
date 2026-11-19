package config

import (
	"errors"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/catimport/catalogrules/namecases"
)

// ut-docs#5894: one shop-name check for every store.name writer.
func TestCheckStoreName(t *testing.T) {
	refused := []struct {
		name string
		in   string
		want error
	}{
		{"rlo", "Shop‮", ErrStoreNameInvalidChars},
		{"line separator", "Shop x", ErrStoreNameInvalidChars},
		{"paragraph separator", "Shop x", ErrStoreNameInvalidChars},
		{"variation selector first", "Shop\U000E0100", ErrStoreNameInvalidChars},
		{"variation selector last", "Shop\U000E01EF", ErrStoreNameInvalidChars},
		{"newline", "a\nb", ErrStoreNameInvalidChars},
		{"tab", "a\tb", ErrStoreNameInvalidChars},
		{"zwsp", "a​b", ErrStoreNameInvalidChars},
		{"unbalanced lri", "a⁦b", ErrStoreNameInvalidChars},
		{"balanced isolate", "a⁦b⁩", ErrStoreNameInvalidChars},
		{"rlm", "a‏b", ErrStoreNameInvalidChars},
		{"invalid utf8", "a\xffb", ErrStoreNameInvalidChars},
		{"empty", "", ErrStoreNameRequired},
		{"spaces", "   ", ErrStoreNameRequired},
		{"placeholder", "My Store", ErrStoreNameRequired},
		{"placeholder spaced lower", " my store ", ErrStoreNameRequired},
		{"default name", DefaultStoreName, ErrStoreNameRequired},
		{"joiner only", "‍", ErrStoreNameRequired},
		{"81 runes", strings.Repeat("é", 81), ErrStoreNameTooLong},
	}
	sentinels := []error{ErrStoreNameRequired, ErrStoreNameTooLong, ErrStoreNameInvalidChars}
	for _, tc := range refused {
		t.Run("refuse/"+tc.name, func(t *testing.T) {
			got, err := CheckStoreName(tc.in)
			if got != "" {
				t.Fatalf("name = %q, want empty on refusal", got)
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			n := 0
			for _, s := range sentinels {
				if errors.Is(err, s) {
					n++
				}
			}
			if n != 1 {
				t.Fatalf("err %v matches %d sentinels, want exactly 1", err, n)
			}
		})
	}

	accepted := []struct{ name, in, want string }{
		{"80 runes", strings.Repeat("é", 80), strings.Repeat("é", 80)},
		{"trimmed", "  Café Ünal  ", "Café Ünal"},
		{"persian zwnj", "می‌خواهم", "می‌خواهم"},
		{"zwj emoji", "\U0001F468‍\U0001F469‍\U0001F467 Shop", "\U0001F468‍\U0001F469‍\U0001F467 Shop"},
		{"fe0f", "Heart ❤️", "Heart ❤️"},
	}
	for _, tc := range accepted {
		t.Run("accept/"+tc.name, func(t *testing.T) {
			got, err := CheckStoreName(tc.in)
			if err != nil || got != tc.want {
				t.Fatalf("got (%q, %v), want (%q, nil)", got, err, tc.want)
			}
		})
	}
}

// The helper is never looser than either rule it composes.
func TestCheckStoreName_SharedNameCases(t *testing.T) {
	for _, c := range namecases.Cases() {
		t.Run(c.Label, func(t *testing.T) {
			got, err := CheckStoreName(c.In)
			if !c.OK {
				if !errors.Is(err, ErrStoreNameInvalidChars) {
					t.Fatalf("err = %v, want ErrStoreNameInvalidChars", err)
				}
				return
			}
			want, wantErr := NormalizeStoreName(c.Want)
			if got != want || !errors.Is(err, wantErr) && (err != nil || wantErr != nil) {
				t.Fatalf("got (%q, %v), want (%q, %v)", got, err, want, wantErr)
			}
		})
	}
}

// UT_STORE_NAME that the helper refuses never reaches cfg.StoreName (the
// kiosk and enrolment read it); UT_MARKETPLACE_STORE_ID's raw fallback is
// deliberately left alone.
func TestInitStoreNameEnv(t *testing.T) {
	cases := []struct{ name, env, want string }{
		{"rlo", "Shop‮", DefaultStoreName},
		{"variation selector", "Shop\U000E0100", DefaultStoreName},
		{"valid", "Corner Cafe", "Corner Cafe"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			unsetForTest(t, configEnvKeys)
			t.Setenv("UT_STORE_NAME", tc.env)
			cfg, err := Init()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.StoreName != tc.want {
				t.Fatalf("StoreName = %q, want %q", cfg.StoreName, tc.want)
			}
			if cfg.Marketplace.StoreID != tc.env {
				t.Fatalf("Marketplace.StoreID = %q, want raw %q", cfg.Marketplace.StoreID, tc.env)
			}
		})
	}
}
