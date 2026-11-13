//go:build wasip1

// Test guest for e2e/tests/plugin-views-4378.spec.ts (ut-docs#4378): a view
// plugin that answers ui.view.ask / ui.action.ask with real v1 documents for
// a chat, a wizard (+ qr) and a long-running job page, and answers its own
// two job events. Installed by e2e/seed_plugin_views; generic names only.
package main

import (
	"fmt"
	"time"

	"github.com/universaltill/universal-till/sdk/plugin"
)

const (
	// The job durations are fixed on purpose: only a poll response can
	// remove the thinking bubble / the poll component, so a poll while they
	// are shown is guaranteed, never a race.
	chatJobMS = 2000
	workJobMS = 3000
	qrData    = "E2E-4378-QR"

	chatEvent = "com.example.e2e-views.chat"
	workEvent = "com.example.e2e-views.work"
	maxTurns  = 10
)

type payload struct {
	View   string         `json:"view"`
	Action string         `json:"action"`
	Form   map[string]any `json:"form"`
	Turns  []turn         `json:"turns"`
}

type turn struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

func main() {
	plugin.Run(plugin.Handlers{"*": handle})
}

func key(k string) map[string]string     { return map[string]string{"key": k} }
func literal(s string) map[string]string { return map[string]string{"literal": s} }

func doc(title string, components ...any) map[string]any {
	d := map[string]any{"version": 1, "components": components}
	if title != "" {
		d["title"] = key(title)
	}
	return map[string]any{"document": d}
}

func chatDoc(turns []map[string]any) map[string]any {
	return doc("e2eviews.chat.title", map[string]any{
		"type": "chat", "turns": turns, "action": "ask",
		"placeholder": key("e2eviews.chat.placeholder"),
	})
}

func lit(role, text string) map[string]any {
	return map[string]any{"role": role, "text": literal(text)}
}

func initialTurns() []map[string]any {
	a := lit("assistant", "Hi there")
	a["tools"] = []any{key("e2eviews.chat.tool")}
	return []map[string]any{lit("user", "Hello"), a, lit("user", "Sales today?"), lit("assistant", "42 cups")}
}

func wizardDoc(n int) map[string]any {
	comps := []any{
		map[string]any{"type": "wizard_step", "current": n, "steps": []any{
			key("e2eviews.wizard.step1"), key("e2eviews.wizard.step2"), key("e2eviews.wizard.step3")}},
		map[string]any{"type": "text", "text": key(fmt.Sprintf("e2eviews.wizard.body%d", n))},
	}
	if n == 1 {
		comps = append(comps, map[string]any{"type": "qr", "data": qrData, "label": key("e2eviews.wizard.qr_label")})
	}
	if n < 3 {
		comps = append(comps, map[string]any{"type": "button", "action": fmt.Sprintf("to_step_%d", n+1),
			"label": key("e2eviews.wizard.next"), "style": "primary"})
	}
	if n > 1 {
		comps = append(comps, map[string]any{"type": "button", "action": fmt.Sprintf("to_step_%d", n-1),
			"label": key("e2eviews.wizard.back"), "style": "secondary"})
	}
	return doc("e2eviews.wizard.title", comps...)
}

func jobDoc() map[string]any {
	return doc("e2eviews.job.title",
		map[string]any{"type": "text", "text": key("e2eviews.job.intro")},
		map[string]any{"type": "button", "action": "run", "label": key("e2eviews.job.run")})
}

func job(event string) map[string]any {
	return map[string]any{"job": map[string]any{"event": event}}
}

func handle(ev plugin.Event) (any, error) {
	var p payload
	if err := ev.Decode(&p); err != nil {
		return nil, err
	}
	switch ev.Type {
	case "ui.view.ask":
		switch p.View {
		case "e2eviews.chat":
			return chatDoc(initialTurns()), nil
		case "e2eviews.wizard":
			return wizardDoc(1), nil
		case "e2eviews.job":
			return jobDoc(), nil
		}
	case "ui.action.ask":
		switch {
		case p.View == "e2eviews.chat" && p.Action == "ask":
			return job(chatEvent), nil
		case p.View == "e2eviews.wizard" && p.Action == "to_step_1":
			return wizardDoc(1), nil
		case p.View == "e2eviews.wizard" && p.Action == "to_step_2":
			return wizardDoc(2), nil
		case p.View == "e2eviews.wizard" && p.Action == "to_step_3":
			return wizardDoc(3), nil
		case p.View == "e2eviews.job" && p.Action == "run":
			return job(workEvent), nil
		}
	case chatEvent:
		time.Sleep(chatJobMS * time.Millisecond)
		q, _ := p.Form["question"].(string)
		turns := make([]map[string]any, 0, len(p.Turns)+2)
		for _, t := range p.Turns {
			turns = append(turns, lit(t.Role, t.Text))
		}
		turns = append(turns, lit("user", q), lit("assistant", "Echo: "+q))
		if len(turns) > maxTurns {
			turns = turns[len(turns)-maxTurns:]
		}
		return chatDoc(turns), nil
	case workEvent:
		if err := plugin.JobProgress(50, "e2eviews.job.progress"); err != nil {
			return nil, err
		}
		time.Sleep(workJobMS * time.Millisecond)
		return doc("e2eviews.job.title",
			map[string]any{"type": "notice", "level": "info", "text": key("e2eviews.job.done")},
			map[string]any{"type": "text", "text": literal("E2E-4378-RESULT")}), nil
	}
	return nil, fmt.Errorf("unhandled %s %s/%s", ev.Type, p.View, p.Action)
}
