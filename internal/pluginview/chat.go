package pluginview

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/httpx"
)

// The chat component (ADR-0121 amendment 2026-10-11, items 2-4;
// ut-docs#4132; format: ut-docs reference/plugin-views.md). Core draws the
// transcript, the tool-use chips and the question box. The till keeps no
// conversation: the question box posts the turns it drew back as core's
// own field _turns (role + text only, bounded), core validates and bounds
// it and hands it to the plugin as the ui.action.ask payload's "turns".
// The history lives only in the open page.

// Chat caps (reference/plugin-views.md "Limits"). A document over any of
// them is refused whole.
const (
	// MaxChatTurns bounds a chat's turns, and the posted history.
	MaxChatTurns = 10
	// MaxChatTextBytes bounds the sum of a chat's turn texts (each turn
	// is also a literal of at most MaxStringBytes).
	MaxChatTextBytes = 8 << 10
	// MaxChatTools bounds one assistant turn's tool-use chips.
	MaxChatTools = 8
	// MaxHistoryBytes bounds _turns as serialized: core drops the oldest
	// turns until the history it draws fits, and drops a posted one over
	// it whole.
	MaxHistoryBytes = 8 << 10
)

// HistoryField is core's own form field that carries a chat's history.
const HistoryField = "_turns"

// Chat is the chat component: the conversation so far, the action the
// question box posts, and an optional placeholder for it.
type Chat struct {
	Type        string     `json:"type"`
	Turns       []ChatTurn `json:"turns"`
	Action      string     `json:"action"`
	Placeholder *Text      `json:"placeholder,omitempty"`
}

// ChatTurn is one turn: Text is always a literal (a conversation is not
// translatable), Tools only on an assistant turn.
type ChatTurn struct {
	Role  string `json:"role"`
	Text  Text   `json:"text"`
	Tools []Text `json:"tools,omitempty"`
}

// HistoryTurn is one turn of the history core posts (_turns) and forwards
// (payload "turns"): role and text, never tool chips.
type HistoryTurn struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

func validRole(r string) bool { return r == "user" || r == "assistant" }

func (v *validator) chat(where string, c *Chat) {
	if v.chats++; v.chats > 1 {
		v.fail("%s: a document has at most one chat", where)
	}
	v.name(where, "action", c.Action)
	if c.Placeholder != nil {
		v.text(where+".placeholder", *c.Placeholder)
	}
	if len(c.Turns) > MaxChatTurns {
		v.fail("%s has %d turns (max %d)", where, len(c.Turns), MaxChatTurns)
	}
	total := 0
	for i, t := range c.Turns {
		w := fmt.Sprintf("%s.turns[%d]", where, i)
		if !validRole(t.Role) {
			v.fail("%s role %q must be user or assistant", w, t.Role)
		}
		if t.Text.Literal == nil || t.Text.Key != "" {
			v.fail("%s.text must be a literal (a turn is not a locale key)", w)
			continue
		}
		v.str(w+".text", *t.Text.Literal)
		total += len(*t.Text.Literal)
		if len(t.Tools) > 0 && t.Role != "assistant" {
			v.fail("%s: tools are only for an assistant turn", w)
		}
		if len(t.Tools) > MaxChatTools {
			v.fail("%s has %d tools (max %d)", w, len(t.Tools), MaxChatTools)
		}
		for j, tl := range t.Tools {
			v.text(fmt.Sprintf("%s.tools[%d]", w, j), tl)
		}
	}
	if total > MaxChatTextBytes {
		v.fail("%s turn text is %d bytes in all (max %d)", where, total, MaxChatTextBytes)
	}
}

// encodeHistory is turns as _turns carries them (no HTML escaping: the
// template escapes the attribute, and the bound counts these bytes).
func encodeHistory(turns []HistoryTurn) string {
	if turns == nil {
		turns = []HistoryTurn{}
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(turns)
	return strings.TrimSuffix(b.String(), "\n")
}

// BoundHistory keeps the newest turns that fit: at most MaxChatTurns, and
// at most MaxHistoryBytes serialized, the oldest dropped first.
func BoundHistory(turns []HistoryTurn) []HistoryTurn {
	if len(turns) > MaxChatTurns {
		turns = turns[len(turns)-MaxChatTurns:]
	}
	for len(turns) > 0 && len(encodeHistory(turns)) > MaxHistoryBytes {
		turns = turns[1:]
	}
	return append([]HistoryTurn{}, turns...)
}

// EncodeHistory is the bounded history as the _turns value.
func EncodeHistory(turns []HistoryTurn) string { return encodeHistory(BoundHistory(turns)) }

// DecodeHistory reads a posted _turns value. It is operator input: a
// value that is not valid UTF-8 JSON of at most MaxHistoryBytes holding
// at most MaxChatTurns {role, text} turns (role user or assistant, text a
// string) is dropped whole. Never nil, never an error.
func DecodeHistory(raw string) []HistoryTurn {
	empty := []HistoryTurn{}
	if raw == "" || len(raw) > MaxHistoryBytes || !utf8.ValidString(raw) {
		return empty
	}
	var turns []*struct {
		Role *string `json:"role"`
		Text *string `json:"text"`
	}
	if strictDecode([]byte(raw), &turns) != nil || turns == nil || len(turns) > MaxChatTurns {
		return empty
	}
	out := make([]HistoryTurn, 0, len(turns))
	for _, t := range turns {
		if t == nil || t.Role == nil || t.Text == nil || !validRole(*t.Role) {
			return empty
		}
		out = append(out, HistoryTurn{Role: *t.Role, Text: *t.Text})
	}
	return out
}

// TakeHistory removes _turns from a post and decodes it. posted reports
// whether the post carried it at all (a chat's question box); a malformed
// or repeated value is an empty history.
func TakeHistory(vals map[string][]string) (turns []HistoryTurn, posted bool) {
	raw, ok := vals[HistoryField]
	if !ok {
		return nil, false
	}
	delete(vals, HistoryField)
	if len(raw) != 1 {
		return []HistoryTurn{}, true
	}
	return DecodeHistory(raw[0]), true
}

// DecodePost reads a posted plugin view form: core's chat history first
// (TakeHistory), then the rest through DecodeForm, which still refuses
// every other "_" field.
func DecodePost(vals map[string][]string, decimals int, currency string) (Submission, error) {
	turns, posted := TakeHistory(vals)
	s, err := DecodeForm(vals, decimals, currency)
	s.Turns, s.TurnsPosted = turns, posted
	return s, err
}

// ViewChat is a drawn chat: the plugin's (Prepare), or core's own interim
// while a job answers a chat post or after it failed (InterimChat).
type ViewChat struct {
	Turns       []ViewTurn
	Action      string
	Placeholder string
	// Question refills the question box (a failed post, ut-docs#3879).
	Question string
	// History is the _turns value the question box posts.
	History string
	// Failed: the job this chat waited for failed; its notice is drawn
	// under the transcript and the question box is back for a retry.
	Failed bool
}

// ViewTurn is one drawn turn; Tools are the tool names, which the
// template draws as "used: <tool>" chips.
type ViewTurn struct {
	Role, Text string
	Tools      []string
}

func prepareChat(c *Chat, locale string, t func(Text) string) *ViewChat {
	vc := &ViewChat{Action: c.Action}
	if c.Placeholder != nil {
		vc.Placeholder = t(*c.Placeholder)
	} else {
		vc.Placeholder = httpx.T(locale, "plugin.chat.placeholder")
	}
	hist := make([]HistoryTurn, 0, len(c.Turns))
	for _, tn := range c.Turns {
		vt := ViewTurn{Role: tn.Role, Text: *tn.Text.Literal}
		for _, tl := range tn.Tools {
			vt.Tools = append(vt.Tools, t(tl))
		}
		vc.Turns = append(vc.Turns, vt)
		hist = append(hist, HistoryTurn{Role: tn.Role, Text: vt.Text})
	}
	vc.History = EncodeHistory(hist)
	return vc
}

// InterimChat is core's own chat while a job answers a chat post: the
// posted turns plus the question as a user turn (a blank question draws
// none). failed: the job failed — the question refills the box and the
// box posts the posted turns again, so sending retries the question.
func InterimChat(locale, action string, turns []HistoryTurn, question string, failed bool) *ViewChat {
	vc := &ViewChat{Action: action, Placeholder: httpx.T(locale, "plugin.chat.placeholder"), Failed: failed}
	for _, tn := range turns {
		vc.Turns = append(vc.Turns, ViewTurn{Role: tn.Role, Text: tn.Text})
	}
	if strings.TrimSpace(question) != "" {
		vc.Turns = append(vc.Turns, ViewTurn{Role: "user", Text: question})
	}
	if failed {
		vc.Question = question
		vc.History = EncodeHistory(turns)
	}
	return vc
}
