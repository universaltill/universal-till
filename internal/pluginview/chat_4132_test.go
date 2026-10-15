package pluginview

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

// ut-docs#4132 (ADR-0121 amendment 2026-10-11, items 2-4): the chat
// component and the core-carried _turns history.

func chatDoc(turns string, extra string) string {
	return doc(`{"type":"chat","turns":[` + turns + `],"action":"ask"` + extra + `}`)
}

func lit(s string) string {
	b, _ := json.Marshal(s)
	return `{"literal":` + string(b) + `}`
}

func turnJSON(role, text string) string {
	return `{"role":"` + role + `","text":` + lit(text) + `}`
}

func TestDecodeViewAnswer_Chat_4132(t *testing.T) {
	raw := chatDoc(
		turnJSON("user", "How much tea did we sell?")+`,`+
			`{"role":"assistant","text":`+lit("42 cups.\nMostly green.")+`,"tools":[{"literal":"sales_report"},{"key":"plugin.demo.hello"}]}`,
		`,"placeholder":{"key":"plugin.demo.hello"}`)
	d, err := DecodeViewAnswer([]byte(raw), testCtx)
	if err != nil {
		t.Fatalf("valid chat refused: %v", err)
	}
	c := d.Components[0].Chat
	if c == nil || len(c.Turns) != 2 || c.Action != "ask" || c.Placeholder == nil {
		t.Fatalf("chat = %+v", c)
	}
	// An empty conversation (a fresh chat) is valid too, placeholder optional.
	if _, err := DecodeViewAnswer([]byte(chatDoc("", "")), testCtx); err != nil {
		t.Fatalf("empty chat refused: %v", err)
	}
	// A chat is answerable from an action too (the plugin's later document).
	if _, err := DecodeActionAnswer([]byte(chatDoc(turnJSON("user", "hi"), "")), testCtx); err != nil {
		t.Fatalf("chat action answer refused: %v", err)
	}
}

func TestDecodeViewAnswer_ChatRefusals_4132(t *testing.T) {
	var eleven []string
	for i := 0; i <= MaxChatTurns; i++ {
		eleven = append(eleven, turnJSON("user", "q"))
	}
	var tools9 []string
	for i := 0; i <= MaxChatTools; i++ {
		tools9 = append(tools9, lit("t"))
	}
	big := strings.Repeat("a", MaxStringBytes) // each at the per-literal cap
	cases := []struct{ name, raw, want string }{
		{"unknown field", chatDoc("", `,"html":"<b>x</b>"`), "unknown field"},
		{"unknown turn field", chatDoc(`{"role":"user","text":`+lit("x")+`,"markdown":true}`, ""), "unknown field"},
		{"two chats", doc(`{"type":"chat","turns":[],"action":"ask"},{"type":"chat","turns":[],"action":"ask"}`), "one chat"},
		{"role system", chatDoc(turnJSON("system", "x"), ""), "role"},
		{"role empty", chatDoc(turnJSON("", "x"), ""), "role"},
		{"too many turns", chatDoc(strings.Join(eleven, ","), ""), "turns"},
		{"total text over 8 KiB", chatDoc(turnJSON("user", big)+","+turnJSON("assistant", big)+","+turnJSON("user", "x"), ""), "bytes"},
		{"turn text is a key", chatDoc(`{"role":"user","text":{"key":"plugin.demo.hello"}}`, ""), "literal"},
		{"turn text over 4 KiB", chatDoc(turnJSON("user", big+"a"), ""), "exceeds"},
		{"turn text empty object", chatDoc(`{"role":"user","text":{}}`, ""), "literal"},
		{"turn text key and literal", chatDoc(`{"role":"user","text":{"key":"plugin.demo.hello","literal":"x"}}`, ""), "literal"},
		{"bad action", doc(`{"type":"chat","turns":[],"action":"Ask!"}`), "action"},
		{"reserved action", doc(`{"type":"chat","turns":[],"action":"_turns"}`), "action"},
		{"missing action", doc(`{"type":"chat","turns":[]}`), "action"},
		{"too many tools", chatDoc(`{"role":"assistant","text":`+lit("a")+`,"tools":[`+strings.Join(tools9, ",")+`]}`, ""), "tools"},
		{"tools on a user turn", chatDoc(`{"role":"user","text":`+lit("a")+`,"tools":[`+lit("t")+`]}`, ""), "tools"},
		{"foreign tool key", chatDoc(`{"role":"assistant","text":`+lit("a")+`,"tools":[{"key":"nav.home"}]}`, ""), "own locale bundle"},
		{"foreign placeholder key", chatDoc("", `,"placeholder":{"key":"nav.home"}`), "own locale bundle"},
		{"placeholder over 4 KiB", chatDoc("", `,"placeholder":`+lit(big+"a")), "exceeds"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeViewAnswer([]byte(tc.raw), testCtx)
			if err == nil {
				t.Fatalf("accepted, want refusal containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// A chat posts to the entry's route: never in a core seam.
func TestDecodeViewAnswer_ChatNotInSeam_4132(t *testing.T) {
	c := testCtx
	c.Seam = SeamItemForm
	if _, err := DecodeViewAnswer([]byte(chatDoc("", "")), c); err == nil || !strings.Contains(err.Error(), "seam") {
		t.Fatalf("chat in a seam: err = %v, want a seam refusal", err)
	}
}

func TestPrepare_Chat_4132(t *testing.T) {
	v := prepared(t, `{"type":"chat","turns":[`+turnJSON("user", "Hi <b>")+`,{"role":"assistant","text":`+lit("Line 1\nLine 2")+`,"tools":[{"literal":"sales"}]}],"action":"ask"}`, "en")
	c := v.Components[0].Chat
	if c == nil {
		t.Fatal("no chat view")
	}
	if c.Action != "ask" || len(c.Turns) != 2 {
		t.Fatalf("chat view = %+v", c)
	}
	if c.Turns[0].Role != "user" || c.Turns[0].Text != "Hi <b>" || c.Turns[1].Role != "assistant" || c.Turns[1].Text != "Line 1\nLine 2" {
		t.Errorf("turns = %+v", c.Turns)
	}
	if len(c.Turns[1].Tools) != 1 || c.Turns[1].Tools[0] != "sales" {
		t.Errorf("tools = %v", c.Turns[1].Tools)
	}
	// No placeholder: core's own.
	if c.Placeholder == "" || c.Placeholder == "plugin.chat.placeholder" {
		t.Errorf("placeholder = %q, want core's translated default", c.Placeholder)
	}
	// The history is role + text only (no tool chips).
	var h []map[string]any
	if err := json.Unmarshal([]byte(c.History), &h); err != nil || len(h) != 2 {
		t.Fatalf("history %q: %v", c.History, err)
	}
	if _, ok := h[1]["tools"]; ok || h[1]["role"] != "assistant" || h[1]["text"] != "Line 1\nLine 2" {
		t.Errorf("history turn = %v", h[1])
	}
	pv := prepared(t, `{"type":"chat","turns":[],"action":"ask","placeholder":{"literal":"Ask me"}}`, "en")
	if got := pv.Components[0].Chat; got.Placeholder != "Ask me" || got.History != "[]" {
		t.Errorf("placeholder %q history %q", got.Placeholder, got.History)
	}
}

// The rendered history is bounded: at most MaxChatTurns turns and
// MaxHistoryBytes serialized, oldest dropped first.
func TestBoundHistory_4132(t *testing.T) {
	var twelve []HistoryTurn
	for i := 0; i < 12; i++ {
		twelve = append(twelve, HistoryTurn{Role: []string{"user", "assistant"}[i%2], Text: "t" + itoa(i)})
	}
	got := BoundHistory(twelve)
	if len(got) != MaxChatTurns || got[0].Text != "t2" || got[len(got)-1].Text != "t11" {
		t.Fatalf("12 turns bounded to %+v, want the last 10 (t2..t11)", got)
	}
	// Text within the validator's caps whose JSON escaping pushes the
	// serialized history over 8 KiB: the oldest go first.
	quotes := strings.Repeat(`"`, 1500) // 3000 bytes once escaped
	turns := []HistoryTurn{{"user", "first"}, {"assistant", quotes}, {"user", quotes}, {"assistant", quotes}, {"user", "last"}}
	got = BoundHistory(turns)
	enc := EncodeHistory(turns)
	if len(enc) > MaxHistoryBytes {
		t.Fatalf("encoded history is %d bytes (max %d)", len(enc), MaxHistoryBytes)
	}
	if len(got) != 3 || got[0].Text != quotes || got[2].Text != "last" {
		t.Fatalf("bounded = %d turns starting %.10q, want the oldest dropped", len(got), got[0].Text)
	}
	if EncodeHistory(nil) != "[]" {
		t.Errorf("empty history encodes as %q", EncodeHistory(nil))
	}
}

// Render-side: a valid 10-turn chat whose serialized history is over
// 8 KiB is drawn whole, but its hidden history drops the oldest turns.
func TestPrepare_ChatHistoryBounded_4132(t *testing.T) {
	quotes := strings.Repeat(`"`, 800)
	var turns []string
	for i := 0; i < MaxChatTurns; i++ {
		turns = append(turns, turnJSON([]string{"user", "assistant"}[i%2], quotes))
	}
	v := prepared(t, `{"type":"chat","turns":[`+strings.Join(turns, ",")+`],"action":"ask"}`, "en")
	c := v.Components[0].Chat
	if len(c.Turns) != MaxChatTurns {
		t.Fatalf("drawn turns = %d, want all %d", len(c.Turns), MaxChatTurns)
	}
	if len(c.History) > MaxHistoryBytes {
		t.Fatalf("history is %d bytes (max %d)", len(c.History), MaxHistoryBytes)
	}
	if h := DecodeHistory(c.History); len(h) == 0 || len(h) >= MaxChatTurns {
		t.Fatalf("history kept %d turns, want fewer than %d but some", len(h), MaxChatTurns)
	}
}

func TestDecodeHistory_4132(t *testing.T) {
	valid := `[{"role":"user","text":"hi"},{"role":"assistant","text":"hello\nthere"}]`
	if got := DecodeHistory(valid); len(got) != 2 || got[1] != (HistoryTurn{"assistant", "hello\nthere"}) {
		t.Fatalf("valid history = %+v", got)
	}
	var eleven []string
	for i := 0; i <= MaxChatTurns; i++ {
		eleven = append(eleven, `{"role":"user","text":"q"}`)
	}
	bad := map[string]string{
		"empty":           ``,
		"not json":        `[{"role":`,
		"object":          `{"role":"user","text":"x"}`,
		"wrong role":      `[{"role":"system","text":"x"}]`,
		"no role":         `[{"text":"x"}]`,
		"number text":     `[{"role":"user","text":5}]`,
		"null text":       `[{"role":"user","text":null}]`,
		"object text":     `[{"role":"user","text":{"literal":"x"}}]`,
		"extra field":     `[{"role":"user","text":"x","tools":["t"]}]`,
		"trailing data":   `[] []`,
		"too many turns":  `[` + strings.Join(eleven, ",") + `]`,
		"over 8 KiB":      `[{"role":"user","text":"` + strings.Repeat("a", MaxHistoryBytes) + `"}]`,
		"null":            `null`,
		"null entry":      `[null]`,
		"invalid utf8":    "[{\"role\":\"user\",\"text\":\"\xff\"}]",
		"nested array":    `[[]]`,
		"string":          `"x"`,
		"huge whitespace": strings.Repeat(" ", MaxHistoryBytes+1) + `[]`,
	}
	for name, raw := range bad {
		t.Run(name, func(t *testing.T) {
			got := DecodeHistory(raw)
			if got == nil || len(got) != 0 {
				t.Fatalf("DecodeHistory(%.40q) = %#v, want an empty, non-nil history", raw, got)
			}
		})
	}
}

// DecodePost strips _turns before DecodeForm; DecodeForm still refuses
// every other "_" field, and _turns itself when called directly.
func TestDecodePost_Turns_4132(t *testing.T) {
	vals := url.Values{"_action": {"ask"}, "question": {"And yesterday?"}, HistoryField: {`[{"role":"user","text":"hi"}]`}}
	sub, err := DecodePost(vals, 2, "EUR")
	if err != nil {
		t.Fatalf("DecodePost: %v", err)
	}
	if !sub.TurnsPosted || len(sub.Turns) != 1 || sub.Turns[0].Text != "hi" {
		t.Fatalf("turns = %v posted %v", sub.Turns, sub.TurnsPosted)
	}
	if sub.Values["question"] != "And yesterday?" {
		t.Errorf("values = %v", sub.Values)
	}
	if _, ok := sub.Values[HistoryField]; ok {
		t.Error("_turns reached the plugin's form values")
	}
	if _, ok := sub.Posted[HistoryField]; ok {
		t.Error("_turns kept in the refill post")
	}

	// Tampered: posted, but empty history — never an error.
	sub, err = DecodePost(url.Values{"_action": {"ask"}, HistoryField: {`[{"role":"root","text":"x"}]`}}, 2, "EUR")
	if err != nil || !sub.TurnsPosted || sub.Turns == nil || len(sub.Turns) != 0 {
		t.Fatalf("tampered: turns %#v posted %v err %v", sub.Turns, sub.TurnsPosted, err)
	}
	// Posted twice: malformed, empty -- the valid value first, so taking
	// either one (not refusing both) fails this.
	sub, err = DecodePost(url.Values{"_action": {"ask"}, HistoryField: {`[{"role":"user","text":"x"}]`, `[]`}}, 2, "EUR")
	if err != nil || !sub.TurnsPosted || len(sub.Turns) != 0 {
		t.Fatalf("twice: turns %#v err %v", sub.Turns, err)
	}
	// Not posted at all: not a chat post.
	sub, err = DecodePost(url.Values{"_action": {"ask"}}, 2, "EUR")
	if err != nil || sub.TurnsPosted || sub.Turns != nil {
		t.Fatalf("no _turns: turns %#v posted %v err %v", sub.Turns, sub.TurnsPosted, err)
	}
	// Every other reserved field is still refused.
	for _, k := range []string{"_x", "_turn", "_turns.x", "_history"} {
		if _, err := DecodePost(url.Values{"_action": {"ask"}, k: {"1"}}, 2, "EUR"); err == nil {
			t.Errorf("DecodePost accepted %q", k)
		}
	}
	if _, err := DecodeForm(url.Values{"_action": {"ask"}, HistoryField: {"[]"}}, 2, "EUR"); err == nil {
		t.Error("DecodeForm accepted _turns directly")
	}
}

func TestInterimChat_4132(t *testing.T) {
	loadI18n(t)
	turns := []HistoryTurn{{"user", "hi"}, {"assistant", "hello"}}
	c := InterimChat("en", "ask", turns, "  and now?  ", false)
	if len(c.Turns) != 3 || c.Turns[2].Role != "user" || c.Turns[2].Text != "  and now?  " {
		t.Fatalf("thinking turns = %+v", c.Turns)
	}
	if c.Failed || c.Question != "" {
		t.Errorf("thinking state: failed %v question %q", c.Failed, c.Question)
	}
	// Failed: the transcript stays, the question refills the box, and the
	// hidden history is the posted turns only (a retry re-asks the question).
	f := InterimChat("en", "ask", turns, "and now?", true)
	if !f.Failed || f.Question != "and now?" || f.Action != "ask" || len(f.Turns) != 3 {
		t.Fatalf("failed state = %+v", f)
	}
	if h := DecodeHistory(f.History); len(h) != 2 {
		t.Errorf("failed history = %q", f.History)
	}
	// A blank question draws no empty bubble.
	if b := InterimChat("en", "ask", nil, "   ", false); len(b.Turns) != 0 {
		t.Errorf("blank question drew %+v", b.Turns)
	}
}

// A no-JS action failure re-asks the view: the chat's question box gets
// what the operator typed back.
func TestRefill_ChatQuestion_4132(t *testing.T) {
	v := prepared(t, `{"type":"chat","turns":[],"action":"ask"}`, "en")
	v.Refill("ask", map[string][]string{"question": {"what sold?"}})
	if got := v.Components[0].Chat.Question; got != "what sold?" {
		t.Errorf("question = %q", got)
	}
	v.Refill("other", map[string][]string{"question": {"x"}})
	if got := v.Components[0].Chat.Question; got != "what sold?" {
		t.Errorf("another action's post refilled the chat: %q", got)
	}
}
