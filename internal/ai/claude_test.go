package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// testClaudeProvider wires the optional hosted provider at a local fake of
// the Messages API — request assembly and response handling get real
// coverage with nothing leaving the test process.
func testClaudeProvider(t *testing.T, handler http.HandlerFunc) *claudeProvider {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &claudeProvider{
		client: anthropic.NewClient(option.WithBaseURL(srv.URL), option.WithAPIKey("test-key"), option.WithMaxRetries(0)),
		model:  anthropic.Model("test-model"),
	}
}

// requestBlock digs the first element out of a captured request-body list
// field, failing with a message (not a panic) if the wire shape changed.
func requestBlock(t *testing.T, body map[string]any, field string) map[string]any {
	t.Helper()
	list, ok := body[field].([]any)
	if !ok || len(list) == 0 {
		t.Fatalf("request %s missing or empty: %+v", field, body[field])
	}
	m, ok := list[0].(map[string]any)
	if !ok {
		t.Fatalf("request %s[0] is not an object: %+v", field, list[0])
	}
	return m
}

func claudeTextResponse(t *testing.T, w http.ResponseWriter, text string, stopReason string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": "msg_test", "type": "message", "role": "assistant", "model": "test-model",
		"content":     []map[string]any{{"type": "text", "text": text}},
		"stop_reason": stopReason,
		"usage":       map[string]any{"input_tokens": 1, "output_tokens": 1},
	})
}

// With reference images, the prompt-cache breakpoint sits on the LAST
// reference image so repeated identifications reuse the catalog+refs prefix.
func TestClaudeIdentifyWithRefs(t *testing.T) {
	var gotBody map[string]any
	p := testClaudeProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/messages") {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		content, _ := json.Marshal(IdentifyResult{Matches: []Candidate{{ItemID: "itm001", Confidence: "high"}}})
		claudeTextResponse(t, w, string(content), "end_turn")
	})

	res, err := p.identify(context.Background(), []byte{9}, "image/jpeg",
		[]CatalogItem{{ID: "itm001", SKU: "S1", Name: "Milk"}},
		[]RefImage{{ItemID: "itm001", MediaType: "image/png", Data: []byte{1}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Matches) != 1 || res.Matches[0].ItemID != "itm001" {
		t.Fatalf("result = %+v", res)
	}

	system := requestBlock(t, gotBody, "system")
	if !strings.Contains(system["text"].(string), "itm001") {
		t.Fatalf("catalog not in system prompt: %v", system["text"])
	}
	if _, cached := system["cache_control"]; cached {
		t.Fatal("with refs, the cache breakpoint belongs on the last ref image, not the system block")
	}
	msg := requestBlock(t, gotBody, "messages")
	blocks, ok := msg["content"].([]any)
	if !ok {
		t.Fatalf("message content missing: %+v", msg)
	}
	// ref label, ref image (cache breakpoint), photo label, photo image
	if len(blocks) != 4 {
		t.Fatalf("content blocks = %d, want 4: %+v", len(blocks), blocks)
	}
	refImg := blocks[1].(map[string]any)
	if refImg["type"] != "image" || refImg["cache_control"] == nil {
		t.Fatalf("last ref image must carry the cache breakpoint: %+v", refImg)
	}
	if gotBody["output_config"] == nil {
		t.Fatal("structured output format not requested")
	}
}

// Without references the breakpoint moves to the system block (still caching
// the catalog context).
func TestClaudeIdentifyNoRefsCachesSystem(t *testing.T) {
	var gotBody map[string]any
	p := testClaudeProvider(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		claudeTextResponse(t, w, `{"matches":[],"suggested_name":"Oat Milk"}`, "end_turn")
	})
	res, err := p.identify(context.Background(), []byte{9}, "image/jpeg", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.SuggestedName != "Oat Milk" || len(res.Matches) != 0 {
		t.Fatalf("result = %+v", res)
	}
	system := requestBlock(t, gotBody, "system")
	if system["cache_control"] == nil {
		t.Fatal("without refs, the system block must carry the cache breakpoint")
	}
}

func TestClaudeIdentifyErrorPaths(t *testing.T) {
	t.Run("refusal", func(t *testing.T) {
		p := testClaudeProvider(t, func(w http.ResponseWriter, r *http.Request) {
			claudeTextResponse(t, w, "", "refusal")
		})
		if _, err := p.identify(context.Background(), []byte{9}, "image/jpeg", nil, nil); err == nil || !strings.Contains(err.Error(), "declined") {
			t.Fatalf("refusal must surface as declined, got %v", err)
		}
	})
	t.Run("no text block", func(t *testing.T) {
		p := testClaudeProvider(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "msg_test", "type": "message", "role": "assistant", "model": "test-model",
				"content": []map[string]any{}, "stop_reason": "end_turn",
				"usage": map[string]any{"input_tokens": 1, "output_tokens": 1},
			})
		})
		if _, err := p.identify(context.Background(), []byte{9}, "image/jpeg", nil, nil); err == nil || !strings.Contains(err.Error(), "empty model response") {
			t.Fatalf("want empty-response error, got %v", err)
		}
	})
	t.Run("unparseable answer", func(t *testing.T) {
		p := testClaudeProvider(t, func(w http.ResponseWriter, r *http.Request) {
			claudeTextResponse(t, w, "not json", "end_turn")
		})
		if _, err := p.identify(context.Background(), []byte{9}, "image/jpeg", nil, nil); err == nil || !strings.Contains(err.Error(), "parse model response") {
			t.Fatalf("want parse error, got %v", err)
		}
	})
	t.Run("api error", func(t *testing.T) {
		p := testClaudeProvider(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad request"}}`))
		})
		if _, err := p.identify(context.Background(), []byte{9}, "image/jpeg", nil, nil); err == nil {
			t.Fatal("api error must propagate")
		}
	})
}
