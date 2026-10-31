package plugins

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/agentaction"
)

// ut-docs#4129 review: the assistant's Ask job is a page-layer job under the
// plugin's own prefix (hop 0, not scheduled, not published by the plugin).
// The gate must deliver it to an ai.agent plugin, or declaring ai.agent
// silently turns "Ask your till" off. The same name raised any other way
// (a manifest-named payment or button trigger) stays refused.
func TestEventDataClass_AIAgentPageLayerJobDelivered_4129(t *testing.T) {
	w, d := evRig(t, evAgentID, []string{"ai.agent"}, nil, nil)
	ev := Event{ID: "job1", Type: evAgentID + ".ask", Timestamp: time.Now(), Payload: []byte(`{}`)}

	jobCtx := WithAskOperator(WithJob(context.Background(), JobCall{}), agentaction.Operator{UserID: "u", Role: "manager", Session: "s", Request: "r"})
	if _, err := w.HandleEvent(jobCtx, evAgentID, ev); err != nil {
		t.Fatalf("page-layer job refused: %v", err)
	}
	if !guestRan(t, d, evAgentID) {
		t.Fatal("the job did not reach the guest")
	}

	// No job on the context: it is just a plugin-prefixed hop-0 event.
	if _, err := w.HandleEvent(context.Background(), evAgentID, ev); !errors.Is(err, ErrEventDataClassDenied) {
		t.Fatalf("prefixed event without a job: err = %v, want ErrEventDataClassDenied", err)
	}
	// A job without an ask operator is not the assistant's.
	if _, err := w.HandleEvent(WithJob(context.Background(), JobCall{}), evAgentID, ev); !errors.Is(err, ErrEventDataClassDenied) {
		t.Fatalf("job without an operator: err = %v, want ErrEventDataClassDenied", err)
	}
}
