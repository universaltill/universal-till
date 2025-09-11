package common

import (
	"os"
	"path/filepath"
	"testing"
)

func TestI18n(t *testing.T) {
	dir := t.TempDir()
	// Setup locale files
	en := `{"hello":"Hello","bye":"Goodbye"}`
	es := `{"hello":"Hola"}`
	if err := os.WriteFile(filepath.Join(dir, "en.json"), []byte(en), 0o600); err != nil {
		t.Fatalf("failed to write en.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "es.json"), []byte(es), 0o600); err != nil {
		t.Fatalf("failed to write es.json: %v", err)
	}

	i, err := NewI18n(dir, "en")
	if err != nil {
		t.Fatalf("NewI18n error: %v", err)
	}

	// Verify translations for specific locales
	tests := []struct {
		locale string
		key    string
		want   string
	}{
		{"en", "hello", "Hello"},
		{"es", "hello", "Hola"},
	}
	for _, tt := range tests {
		t.Run(tt.locale, func(t *testing.T) {
			if got := i.T(tt.locale, tt.key); got != tt.want {
				t.Errorf("T(%s,%s)=%q, want %q", tt.locale, tt.key, got, tt.want)
			}
		})
	}

	// Verify fallback locale
	if got := i.T("es", "bye"); got != "Goodbye" {
		t.Errorf("fallback locale: got %q, want %q", got, "Goodbye")
	}

	// Verify missing keys
	if got := i.T("es", "unknown"); got != "unknown" {
		t.Errorf("missing key: got %q, want %q", got, "unknown")
	}
}
