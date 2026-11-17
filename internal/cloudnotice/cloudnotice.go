// Package cloudnotice is the till's defence-in-depth for notices pulled from
// the cloud (ADR-0176, ut-docs#5366): re-validation of every field the cloud
// sends, the language pick, the "have" hash of the held set, and the one
// renderer for a notice body (plain text plus a tiny markdown subset). It is
// pure: no I/O, no SQL. The cloud's validation is never trusted.
package cloudnotice

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"html/template"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Limits from ADR-0176 §3/§4/§6.
const (
	MaxTitle      = 80
	MaxBody       = 1000
	MaxSenderName = 80
	MaxLabel      = 40
	MaxLifetime   = 90 * 24 * time.Hour
	// MaxStartAhead: starts_at may be at most 30 days ahead (§6); the cloud
	// only lists started items, so anything further out is a bad cloud.
	MaxStartAhead = 30 * 24 * time.Hour
	// MaxItems is the cloud's cap per pull; the till refuses more.
	MaxItems = 20
	// MaxItemBytes bounds one serialised item (ADR-0176 §4).
	MaxItemBytes = 4 << 10
)

// FallbackLang is the language every notice must carry and the last fallback
// (ADR-0176 §3/§4). A wire language tag, not a country.
const FallbackLang = "en-gb" // core-neutral:allow a language tag from the ADR-0176 wire contract, not a country rule

// Severity values.
const (
	Info      = "info"
	Important = "important"
	Critical  = "critical"
)

// Sender kinds.
const (
	SenderPlatform = "platform"
	SenderPartner  = "partner"
)

// Text is one language's title and body.
type Text struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// Link is the optional action of a notice.
type Link struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
	Label  string `json:"label,omitempty"`
}

// Sender is the server-built sender (never taken from message text).
type Sender struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// Item is one notice as the cloud sends it.
type Item struct {
	ID        string          `json:"id"`
	Rev       int             `json:"rev"`
	Sender    Sender          `json:"sender"`
	Severity  string          `json:"severity"`
	Texts     map[string]Text `json:"texts"`
	Link      *Link           `json:"link,omitempty"`
	StartsAt  time.Time       `json:"starts_at"`
	ExpiresAt time.Time       `json:"expires_at"`
	CreatedAt time.Time       `json:"created_at"`
}

// RouteAllowList is the fixed set of till routes a notice's "route" link may
// open (ADR-0176 §6). A route outside it is refused.
var RouteAllowList = map[string]bool{
	"/": true, "/menu": true, "/inventory": true, "/orders": true,
	"/settings": true, "/reports": true, "/help": true, "/plugins": true,
	"/notices": true,
}

var idRe = regexp.MustCompile(`^[0-9A-Za-z-]{1,64}$`)

// Validate re-checks an item in full, link included. A non-nil error means the
// item is unusable as sent. The till itself calls Clean, which treats a bad
// link as droppable rather than fatal.
func Validate(it Item, now time.Time) error {
	if err := validateCore(it, now); err != nil {
		return err
	}
	return validateLink(it)
}

// Clean is what the till applies to a notice from the cloud (ADR-0176 §6): a
// defect in the notice itself drops the notice, but an invalid link (not
// allow-listed, foreign host, over 300 bytes, unknown kind, a bad label, or
// any link from a partner) drops only the LINK. The message, above all a
// critical one, still reaches the shop. The input is not modified.
func Clean(it Item, now time.Time) (Item, error) {
	err := Validate(it, now)
	if err == nil || it.Link == nil {
		return it, err
	}
	// Retry without the link: if the notice is fine then, the link was the
	// only defect and only it goes.
	it.Link = nil
	if err := Validate(it, now); err != nil {
		return it, err
	}
	return it, nil
}

func validateLink(it Item) error {
	if it.Link == nil {
		return nil
	}
	if it.Sender.Kind == SenderPartner {
		return errors.New("partner link") // ADR-0176 §6: partners get no links in v1
	}
	return checkLink(*it.Link)
}

func validateCore(it Item, now time.Time) error {
	if !idRe.MatchString(it.ID) {
		return errors.New("bad id")
	}
	if it.Rev < 0 {
		return errors.New("bad rev")
	}
	switch it.Severity {
	case Info, Important, Critical:
	default:
		return errors.New("bad severity")
	}
	switch it.Sender.Kind {
	case SenderPlatform, SenderPartner:
	default:
		return errors.New("bad sender kind")
	}
	if n := utf8.RuneCountInString(it.Sender.Name); n == 0 || n > MaxSenderName {
		return errors.New("bad sender name")
	}
	if !controlFree(it.Sender.Name, false) {
		return errors.New("control characters in sender name")
	}
	// D4: a critical message can only come from the platform.
	if it.Sender.Kind == SenderPartner && it.Severity == Critical {
		return errors.New("partner critical")
	}
	if !it.ExpiresAt.After(it.StartsAt) || it.ExpiresAt.Sub(it.StartsAt) > MaxLifetime {
		return errors.New("bad lifetime")
	}
	if it.StartsAt.Sub(now) > MaxStartAhead {
		return errors.New("starts too far ahead")
	}
	if len(it.Texts) == 0 {
		return errors.New("no texts")
	}
	if _, ok := it.Texts[FallbackLang]; !ok {
		return errors.New("no en-gb text")
	}
	partner := it.Sender.Kind == SenderPartner
	for lang, tx := range it.Texts {
		if len(lang) == 0 || len(lang) > 16 {
			return errors.New("bad language tag")
		}
		title, body := norm.NFC.String(tx.Title), norm.NFC.String(tx.Body)
		if n := utf8.RuneCountInString(strings.TrimSpace(title)); n == 0 || n > MaxTitle {
			return errors.New("bad title length")
		}
		if n := utf8.RuneCountInString(body); n > MaxBody {
			return errors.New("bad body length")
		}
		if err := checkContent(title, false, partner); err != nil {
			return err
		}
		if err := checkContent(body, true, partner); err != nil {
			return err
		}
	}
	return nil
}

var (
	htmlish  = regexp.MustCompile(`<[A-Za-z/!?]`)
	imageMD  = regexp.MustCompile(`!\[`)
	headingR = regexp.MustCompile(`(?m)^\s{0,3}#{1,6}\s`)
	tableR   = regexp.MustCompile(`(?m)^\s*\|.*\|\s*$`)
	linkMD   = regexp.MustCompile(`\[([^\]\n]{1,80})\]\(([^)\s]{1,300})\)`)
)

// controlFree refuses control and invisible formatting characters (Cc, Cf,
// Zl, Zp): a bidi override (U+202E) can reverse displayed text or disguise
// link text, a zero-width space can split a title or slip past a word
// filter. The joiners and marks real text needs stay allowed: ZWNJ/ZWJ
// (Persian, Arabic, emoji sequences) and LRM/RLM/ALM (mixed-direction text).
// Mirrors the cloud's check (ut-cloud, ADR-0176 §6); the till never trusts it.
func controlFree(s string, allowNewline bool) bool {
	for _, r := range s {
		if r == '\n' && allowNewline {
			continue
		}
		switch r {
		case 0x200C, 0x200D, 0x200E, 0x200F, 0x061C:
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
			return false
		}
	}
	return true
}

func checkContent(s string, markdown, partner bool) error {
	if !controlFree(s, markdown) {
		return errors.New("control characters not allowed")
	}
	if htmlish.MatchString(s) {
		return errors.New("html not allowed")
	}
	if !markdown {
		return nil
	}
	if imageMD.MatchString(s) || headingR.MatchString(s) || tableR.MatchString(s) {
		return errors.New("markdown not allowed")
	}
	for _, m := range linkMD.FindAllStringSubmatch(s, -1) {
		if partner {
			return errors.New("partner link")
		}
		if !safeHTTPS(m[2]) {
			return errors.New("unsafe link in body")
		}
	}
	return nil
}

func checkLink(l Link) error {
	if utf8.RuneCountInString(l.Label) > MaxLabel {
		return errors.New("link label too long")
	}
	if !controlFree(l.Label, false) {
		return errors.New("control characters in link label")
	}
	switch l.Kind {
	case "route":
		if !RouteAllowList[l.Target] {
			return errors.New("route not allowed")
		}
	case "https":
		if !safeHTTPS(l.Target) {
			return errors.New("unsafe link")
		}
	default:
		return errors.New("unknown link kind")
	}
	return nil
}

// safeHTTPS: https only, no credentials, a universaltill.com host, no
// whitespace or control characters (ADR-0176 §6).
func safeHTTPS(raw string) bool {
	if raw == "" || len(raw) > 300 {
		return false
	}
	for _, r := range raw {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "universaltill.com" || strings.HasSuffix(h, ".universaltill.com")
}

// PickText chooses the text for the till's UI locale: exact tag, then the
// same language, then en-gb, then any text at all.
func PickText(texts map[string]Text, lang string) (string, Text, bool) {
	lang = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(lang), "_", "-"))
	if lang != "" {
		if t, ok := texts[lang]; ok {
			return lang, t, true
		}
		base := lang
		if i := strings.IndexByte(base, '-'); i > 0 {
			base = base[:i]
		}
		keys := make([]string, 0, len(texts))
		for k := range texts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if k == base || strings.HasPrefix(k, base+"-") {
				return k, texts[k], true
			}
		}
	}
	if t, ok := texts[FallbackLang]; ok {
		return FallbackLang, t, true
	}
	keys := make([]string, 0, len(texts))
	for k := range texts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return "", Text{}, false
	}
	return keys[0], texts[keys[0]], true
}

// Pair is one held "id:rev".
type Pair struct {
	ID  string
	Rev int
}

// Have is the sha256 hex of the sorted "id:rev" pairs; "" for an empty set
// (ADR-0176 §1.3, §4).
func Have(pairs []Pair) string {
	if len(pairs) == 0 {
		return ""
	}
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = p.ID + ":" + strconv.Itoa(p.Rev)
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])
}

var (
	boldRe   = regexp.MustCompile(`\*\*([^*\n]+?)\*\*`)
	italicRe = regexp.MustCompile(`\*([^*\n]+?)\*`)
)

// RenderBody renders a body: escape first, then allow-list. Only bold,
// italic, https links to an allowed host and line breaks come out as markup.
func RenderBody(body string) template.HTML {
	body = strings.ReplaceAll(norm.NFC.String(body), "\x00", "") // NUL is our token delimiter
	var links []string
	// Pull links out first so their text and URL are escaped exactly once and
	// never touched by the emphasis rules.
	withTokens := linkMD.ReplaceAllStringFunc(body, func(m string) string {
		sub := linkMD.FindStringSubmatch(m)
		if !safeHTTPS(sub[2]) {
			return m // stays literal text, escaped below
		}
		links = append(links, `<a href="`+html.EscapeString(sub[2])+`" target="_blank" rel="noopener noreferrer">`+html.EscapeString(sub[1])+`</a>`)
		return "\x00L" + strconv.Itoa(len(links)-1) + "\x00"
	})
	s := html.EscapeString(withTokens)
	s = boldRe.ReplaceAllString(s, "<strong>$1</strong>")
	s = italicRe.ReplaceAllString(s, "<em>$1</em>")
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "<br>")
	for i, l := range links {
		s = strings.ReplaceAll(s, "\x00L"+strconv.Itoa(i)+"\x00", l)
	}
	return template.HTML(s) //nolint:gosec // built only from escaped text and fixed tags
}

// Describe is a short log-safe label for a dropped item (ids only, never text).
func Describe(it Item) string { return fmt.Sprintf("%s:%d", it.ID, it.Rev) }

// SeverityRank orders severities as the cloud lists them (ADR-0176 §4):
// critical 0, important 1, info 2; anything else last.
func SeverityRank(sev string) int {
	switch sev {
	case Critical:
		return 0
	case Important:
		return 1
	case Info:
		return 2
	}
	return 3
}

// SortForCap orders items severity-first, then newest first, then by id, so
// that when a response carries more than MaxItems the till's truncation keeps
// every critical before any info (ADR-0176 §4 order), whatever order the
// cloud sent them in. Deterministic for the same input.
func SortForCap(items []Item) {
	sort.SliceStable(items, func(i, j int) bool {
		ri, rj := SeverityRank(items[i].Severity), SeverityRank(items[j].Severity)
		if ri != rj {
			return ri < rj
		}
		if !items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].CreatedAt.After(items[j].CreatedAt)
		}
		return items[i].ID < items[j].ID
	})
}

// Settings keys (ADR-0176 §11, §12). SettingEnabled is the per-till flag,
// default off; the other two are the client's small cursor: the opaque rev of
// the last pull and the language the held texts were fetched in.
const (
	SettingEnabled = "notices.enabled"
	SettingRev     = "notices.rev"
	SettingLang    = "notices.lang"
)

// Truthy reports whether a stored setting value means "on".
func Truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}

// WireLang normalises a UI locale ("de", "pt_BR", "en") to the wire tag
// ("de", "pt-br", "en-gb"): lower case, dash separated; bare "en" is en-gb,
// the language every notice must carry.
func WireLang(l string) string {
	l = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(l), "_", "-"))
	if l == "" || l == "en" {
		return FallbackLang
	}
	return l
}

// SafeLink re-checks a link at render time (defence in depth: the stored
// JSON is only ever turned into an <a> through this).
func SafeLink(l Link) bool { return checkLink(l) == nil }

// ParseLink decodes a stored link_json; nil for none or one that is no
// longer safe.
func ParseLink(raw string) *Link {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var l Link
	if err := json.Unmarshal([]byte(raw), &l); err != nil || !SafeLink(l) {
		return nil
	}
	return &l
}
