package pages

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#5410: the client formatter (window.utCurrency.format in app.js)
// must write the same amount as Go for every registry currency in every
// display mode. The sell screen, tender and cash-count fields use the JS
// twin; a code prefix ("AED 12.50") is where the two could drift.
func TestUtCurrencyFormat_MatchesGoForEveryCurrencyAndMode(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	chdirRoot(t)
	raw, err := os.ReadFile("web/public/app.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	start := strings.Index(src, "window.utCurrency = (function(){")
	if start < 0 {
		t.Fatal("app.js has no window.utCurrency IIFE")
	}
	end := strings.Index(src[start:], "\n})();")
	if end < 0 {
		t.Fatal("cannot find the end of the utCurrency IIFE")
	}
	iife := src[start : start+end+len("\n})();")]

	type tc struct {
		Code, Mode, Locale string
		Display            string
		Suffix             bool
		Decimals           int
		Digits, Want       string
		Dir                string
	}
	defer httpx.SnapshotStateForTests()()
	var cases []tc
	for _, c := range httpx.Currencies() {
		for _, mode := range []string{"auto", "symbol", "code"} {
			httpx.InitCurrency(c.Code)
			httpx.InitCurrencyDisplay(mode)
			a := httpx.ActiveCurrency()
			cases = append(cases, tc{Code: c.Code, Mode: mode, Locale: "en", Dir: "ltr",
				Display: a.Display, Suffix: a.Suffix, Decimals: a.Decimals,
				Want: httpx.FormatMoney(1250, "en")})
			// RTL page: a positive prefix word/code is one LTR isolate (the
			// twin of FormatMoneyDisplay); a bare symbol or suffix is not.
			want := httpx.FormatMoney(1250, "en")
			if !a.Suffix && len([]rune(strings.TrimSpace(a.Display))) > 1 {
				want = "\u2066" + want + "\u2069"
			}
			cases = append(cases, tc{Code: c.Code, Mode: mode + "/rtl", Locale: "en", Dir: "rtl",
				Display: a.Display, Suffix: a.Suffix, Decimals: a.Decimals, Want: want})
		}
	}
	in, _ := json.Marshal(cases)
	script := `
const cases = ` + string(in) + `;
const out = [];
for (const c of cases) {
  const window = {};
  const document = { body: { dataset: { currencyDecimals: String(c.Decimals), currencyDisplay: c.Display, currencySuffix: c.Suffix ? '1' : '' } }, documentElement: { dir: c.Dir } };
  new Function('window', 'document', ` + "`" + `"use strict";` + "`" + ` + ` + "`" + `\n` + "`" + ` + ` + string(mustJSON(iife)) + `)(window, document);
  out.push(window.utCurrency.format(1250));
}
console.log(JSON.stringify(out));
`
	f := filepath.Join(t.TempDir(), "parity.js")
	if err := os.WriteFile(f, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := exec.Command(node, f).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, b)
	}
	var got []string
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("node output %q: %v", b, err)
	}
	if len(got) != len(cases) {
		t.Fatalf("got %d results for %d cases", len(got), len(cases))
	}
	for i, c := range cases {
		if got[i] != c.Want {
			t.Errorf("%s/%s: JS %q, Go %q", c.Code, c.Mode, got[i], c.Want)
		}
	}
}

func mustJSON(s string) []byte {
	b, _ := json.Marshal(s)
	return b
}
