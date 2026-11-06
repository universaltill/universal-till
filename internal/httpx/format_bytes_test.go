package httpx

import (
	"bytes"
	"html/template"
	"testing"
)

// ut-docs#3122: sizes in Settings → Storage.
func TestFormatBytes(t *testing.T) {
	InitI18n(realI18n(t), "en")
	cases := []struct {
		n      int64
		locale string
		want   string
	}{
		{0, "en", "0 B"},
		{512, "en", "512 B"},
		{1023, "en", "1,023 B"},
		{1024, "en", "1.0 KB"},
		{1536, "en", "1.5 KB"},
		{5 << 20, "en", "5.0 MB"},
		{(32 << 30) / 10, "en", "3.2 GB"},
		{(11 << 40) / 10, "en", "1.1 TB"},
		{-5, "en", "0 B"},
		{1536, "de", "1,5 KB"},
		{1536, "fa", "۱٫۵ KB"},
	}
	for _, c := range cases {
		if got := FormatBytes(c.n, c.locale); got != c.want {
			t.Errorf("FormatBytes(%d, %q) = %q, want %q", c.n, c.locale, got, c.want)
		}
	}
}

func TestFuncsForExposesBytes(t *testing.T) {
	InitI18n(realI18n(t), "en")
	for _, fm := range []template.FuncMap{FuncsFor("en"), baseFuncs} {
		if _, ok := fm["bytes"]; !ok {
			t.Fatal("bytes template func missing")
		}
		tpl, err := template.New("x").Funcs(fm).Parse(`{{ bytes 1536 }}`)
		if err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		if err := tpl.Execute(&b, nil); err != nil {
			t.Fatal(err)
		}
		if b.String() != "1.5 KB" {
			t.Errorf("{{ bytes 1536 }} = %q", b.String())
		}
	}
}
