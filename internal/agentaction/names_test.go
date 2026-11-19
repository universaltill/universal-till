package agentaction

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/universaltill/universal-till/catimport/catalogrules/namecases"
	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#5909: an agent-proposed item name is held to the shared
// single-line rule (data.CheckSingleLineName), exactly like my. and the
// till's own screens.
func TestParse_NamesFollowSharedRule_5909(t *testing.T) {
	one := int64(1)
	for _, c := range namecases.Cases() {
		t.Run(c.Label, func(t *testing.T) {
			// Direct: covers invalid UTF-8, which a JSON round trip would
			// turn into U+FFFD.
			got, err := cleanName(c.In, itemNameMaxRunes)
			if c.OK {
				if err != nil || got != c.Want {
					t.Fatalf("cleanName = %q, %v; want %q", got, err, c.Want)
				}
			} else if !errors.Is(err, ErrInvalid) || !errors.Is(err, data.ErrNameInvalidCharacters) {
				t.Fatalf("cleanName err = %v, want ErrInvalid and ErrNameInvalidCharacters", err)
			}
			if !utf8.ValidString(c.In) {
				return
			}
			in := c.In
			cb, _ := json.Marshal(createArgs{Items: []createItem{{Name: in, PriceMinor: &one}}})
			ub, _ := json.Marshal(updateArgs{Changes: []updateChange{{ItemID: "a", Name: &in}}})
			cv, cerr := parseCreate(cb)
			uv, uerr := parseUpdate(ub)
			if c.OK {
				if cerr != nil || cv.(createArgs).Items[0].Name != c.Want {
					t.Fatalf("parseCreate = %+v, %v; want %q", cv, cerr, c.Want)
				}
				if uerr != nil || *uv.(updateArgs).Changes[0].Name != c.Want {
					t.Fatalf("parseUpdate = %+v, %v; want %q", uv, uerr, c.Want)
				}
				return
			}
			for _, x := range []struct {
				err  error
				path string
			}{{cerr, "items[0].name"}, {uerr, "changes[0].name"}} {
				if !errors.Is(x.err, ErrInvalid) || !errors.Is(x.err, data.ErrNameInvalidCharacters) ||
					!strings.Contains(x.err.Error(), x.path) {
					t.Errorf("err = %v, want ErrInvalid + ErrNameInvalidCharacters naming %s", x.err, x.path)
				}
			}
		})
	}
}

func TestParse_NameBlankAndLong_5909(t *testing.T) {
	for _, n := range []string{"", "   ", strings.Repeat("a", itemNameMaxRunes+1)} {
		_, err := cleanName(n, itemNameMaxRunes)
		if !errors.Is(err, ErrInvalid) || errors.Is(err, data.ErrNameInvalidCharacters) {
			t.Errorf("cleanName(%q) = %v, want plain ErrInvalid", n, err)
		}
	}
	if _, err := cleanName(strings.Repeat("a", itemNameMaxRunes), itemNameMaxRunes); err != nil {
		t.Errorf("120 runes refused: %v", err)
	}
}

func TestApply_ShapedNamesStoredTrimmed_5909(t *testing.T) {
	ctx := context.Background()
	d, repo := updateFixture(t)
	env := Env{DB: d}
	zwnj := "می‌خواهم"
	emoji := "\U0001F468‍\U0001F469‍\U0001F467 Pack"

	ca, _ := Lookup("catalog.item.create.v1")
	raw, _ := json.Marshal(createArgs{Items: []createItem{
		{Name: "  " + zwnj + "  ", PriceMinor: ptr64(100)},
		{Name: emoji, PriceMinor: ptr64(200)},
	}})
	args, _, state, err := ca.Prepare(ctx, env, raw)
	if err != nil {
		t.Fatalf("create prepare: %v", err)
	}
	if _, err := ca.Apply(ctx, env, args, state, "u", nil); err != nil {
		t.Fatalf("create apply: %v", err)
	}
	var n int
	for _, want := range []string{zwnj, emoji} {
		if err := d.QueryRow(`SELECT COUNT(*) FROM items WHERE name = ?`, want).Scan(&n); err != nil || n != 1 {
			t.Fatalf("items named %q = %d (%v), want 1", want, n, err)
		}
	}

	id := mkItem(t, repo, "Tea", 100)
	ua, _ := Lookup("catalog.item.update.v1")
	raw, _ = json.Marshal(updateArgs{Changes: []updateChange{{ItemID: id, Name: &zwnj}}})
	args, _, state, err = ua.Prepare(ctx, env, raw)
	if err != nil {
		t.Fatalf("update prepare: %v", err)
	}
	if _, err := ua.Apply(ctx, env, args, state, "u", nil); err != nil {
		t.Fatalf("update apply: %v", err)
	}
	if it, _, _ := repo.GetItem(ctx, id); it.Name != zwnj {
		t.Fatalf("renamed to %q, want %q", it.Name, zwnj)
	}
}

func ptr64(v int64) *int64 { return &v }
