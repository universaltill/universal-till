package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testOpenAIProvider wires the openai hosted provider at a local fake of the
// Chat Completions API — request assembly and response handling get real
// coverage with nothing leaving the test process.
func testOpenAIProvider(t *testing.T, handler http.HandlerFunc) *openaiProvider {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &openaiProvider{
		apiKey:      "test-key",
		visionModel: "test-vision-model",
		askModel:    "test-ask-model",
		baseURL:     srv.URL,
		client:      &http.Client{},
	}
}

func TestOpenAIIdentify(t *testing.T) {
	var gotBody map[string]any
	var gotAuth string
	p := testOpenAIProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("path = %s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		content, _ := json.Marshal(IdentifyResult{Matches: []Candidate{{ItemID: "itm001", Confidence: "high"}}})
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": string(content)}}},
		})
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
	if gotAuth != "Bearer test-key" {
		t.Fatalf("authorization header = %q, want Bearer test-key", gotAuth)
	}
	if gotBody["model"] != "test-vision-model" {
		t.Fatalf("model = %v, want the vision model", gotBody["model"])
	}
	msgs, ok := gotBody["messages"].([]any)
	if !ok || len(msgs) != 2 {
		t.Fatalf("messages = %+v, want system+user", gotBody["messages"])
	}
	system := msgs[0].(map[string]any)
	if system["role"] != "system" || !strings.Contains(system["content"].(string), "itm001") {
		t.Fatalf("catalog not in system prompt: %+v", system)
	}
	user := msgs[1].(map[string]any)
	content, ok := user["content"].([]any)
	if !ok {
		t.Fatalf("user content not an array: %+v", user["content"])
	}
	// ref label, ref image, photo label, photo image
	if len(content) != 4 {
		t.Fatalf("content parts = %d, want 4: %+v", len(content), content)
	}
	refImg := content[1].(map[string]any)
	if refImg["type"] != "image_url" {
		t.Fatalf("ref image part type = %v, want image_url", refImg["type"])
	}
	refURL := refImg["image_url"].(map[string]any)["url"].(string)
	if !strings.HasPrefix(refURL, "data:image/png;base64,") {
		t.Fatalf("ref image url = %q, want a data: URL with the ref media type", refURL)
	}
	photoImg := content[3].(map[string]any)
	photoURL := photoImg["image_url"].(map[string]any)["url"].(string)
	if !strings.HasPrefix(photoURL, "data:image/jpeg;base64,") {
		t.Fatalf("photo url = %q, want a data: URL with the photo media type", photoURL)
	}
	rf, ok := gotBody["response_format"].(map[string]any)
	if !ok || rf["type"] != "json_schema" {
		t.Fatalf("structured output not requested: %+v", gotBody["response_format"])
	}
	schema, ok := rf["json_schema"].(map[string]any)
	if !ok || schema["strict"] != true || schema["schema"] == nil {
		t.Fatalf("json_schema malformed: %+v", rf["json_schema"])
	}
}

func TestOpenAIIdentifyNoRefs(t *testing.T) {
	p := testOpenAIProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": `{"matches":[],"suggested_name":"Oat Milk"}`}}},
		})
	})
	res, err := p.identify(context.Background(), []byte{9}, "image/jpeg", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.SuggestedName != "Oat Milk" || len(res.Matches) != 0 {
		t.Fatalf("result = %+v", res)
	}
}

func TestOpenAIIdentifyErrorPaths(t *testing.T) {
	t.Run("refusal", func(t *testing.T) {
		p := testOpenAIProvider(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "refusal": "cannot help with that"}}},
			})
		})
		if _, err := p.identify(context.Background(), []byte{9}, "image/jpeg", nil, nil); err == nil || !strings.Contains(err.Error(), "declined") {
			t.Fatalf("refusal must surface as declined, got %v", err)
		}
	})
	t.Run("no choices", func(t *testing.T) {
		p := testOpenAIProvider(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{}})
		})
		if _, err := p.identify(context.Background(), []byte{9}, "image/jpeg", nil, nil); err == nil || !strings.Contains(err.Error(), "empty model response") {
			t.Fatalf("want empty-response error, got %v", err)
		}
	})
	t.Run("empty content", func(t *testing.T) {
		p := testOpenAIProvider(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": ""}}},
			})
		})
		if _, err := p.identify(context.Background(), []byte{9}, "image/jpeg", nil, nil); err == nil || !strings.Contains(err.Error(), "empty model response") {
			t.Fatalf("want empty-response error, got %v", err)
		}
	})
	t.Run("unparseable answer", func(t *testing.T) {
		p := testOpenAIProvider(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "not json"}}},
			})
		})
		if _, err := p.identify(context.Background(), []byte{9}, "image/jpeg", nil, nil); err == nil || !strings.Contains(err.Error(), "parse model response") {
			t.Fatalf("want parse error, got %v", err)
		}
	})
	t.Run("api error", func(t *testing.T) {
		p := testOpenAIProvider(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid api key","type":"invalid_request_error"}}`))
		})
		if _, err := p.identify(context.Background(), []byte{9}, "image/jpeg", nil, nil); err == nil {
			t.Fatal("api error must propagate")
		}
	})
}

// The tool-call round trip pins the real, load-bearing OpenAI wire detail:
// tool_calls[].function.arguments is a JSON-encoded STRING (unlike Ollama's
// object), and the assistant message (with its tool_calls) must be echoed
// back verbatim, followed by one role:"tool" message per call carrying the
// matching tool_call_id.
func TestOpenAIAskToolLoop(t *testing.T) {
	var gotArgs map[string]any
	call := 0
	p := testOpenAIProvider(t, func(w http.ResponseWriter, r *http.Request) {
		call++
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req["model"] != "test-ask-model" {
			t.Errorf("ask used model %q, want the ask model", req["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		switch call {
		case 1:
			tools, _ := req["tools"].([]any)
			if len(tools) != 1 {
				t.Errorf("tools not sent: %+v", req["tools"])
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"sales_by_day","arguments":"{\"days\":7}"}}]}}]}`))
		case 2:
			msgs, ok := req["messages"].([]any)
			if !ok || len(msgs) < 2 {
				t.Fatalf("messages missing: %+v", req["messages"])
			}
			assistant := msgs[len(msgs)-2].(map[string]any)
			tcs, ok := assistant["tool_calls"].([]any)
			if !ok || len(tcs) != 1 {
				t.Fatalf("assistant tool_calls not echoed back: %+v", assistant)
			}
			tc := tcs[0].(map[string]any)
			if tc["id"] != "call_1" {
				t.Errorf("echoed tool call id = %v, want call_1", tc["id"])
			}
			fn := tc["function"].(map[string]any)
			args, isString := fn["arguments"].(string)
			if !isString {
				t.Fatalf("echoed arguments must stay a JSON STRING, got %T: %v", fn["arguments"], fn["arguments"])
			}
			if !strings.Contains(args, "7") {
				t.Errorf("echoed arguments lost their content: %q", args)
			}
			last := msgs[len(msgs)-1].(map[string]any)
			if last["role"] != "tool" || last["tool_call_id"] != "call_1" {
				t.Fatalf("tool result message malformed: %+v", last)
			}
			if !strings.Contains(last["content"].(string), "12345") {
				t.Errorf("tool result content missing: %+v", last)
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"You took £123.45 today."}}]}`))
		default:
			t.Fatalf("unexpected extra round %d", call)
		}
	})
	tools := []AskTool{{
		Name:        "sales_by_day",
		Description: "daily sales",
		Params:      map[string]any{"type": "object"},
		Run: func(_ context.Context, args map[string]any) (any, error) {
			gotArgs = args
			return []map[string]any{{"day": "2026-07-14", "total": 12345}}, nil
		},
	}}
	answer, err := p.ask(context.Background(), "system prompt", "how did we do today?", tools)
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if !strings.Contains(answer, "123.45") {
		t.Errorf("answer = %q", answer)
	}
	// The JSON-string arguments must decode back into usable args for Run.
	if gotArgs["days"] != float64(7) {
		t.Errorf("tool args = %+v, want days=7 decoded from the arguments string", gotArgs)
	}
}

func TestOpenAIAskLoopBounded(t *testing.T) {
	p := testOpenAIProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"sales_by_day","arguments":"{}"}}]}}]}`))
	})
	tools := []AskTool{{
		Name: "sales_by_day", Description: "x", Params: map[string]any{"type": "object"},
		Run: func(context.Context, map[string]any) (any, error) { return "ok", nil },
	}}
	if _, err := p.ask(context.Background(), "sys", "loop forever", tools); err == nil {
		t.Fatal("unbounded tool loop should error")
	}
}

func TestOpenAIAskEmptyFinalAnswer(t *testing.T) {
	p := testOpenAIProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"   "}}]}`))
	})
	if _, err := p.ask(context.Background(), "sys", "q", nil); err == nil || !strings.Contains(err.Error(), "empty model response") {
		t.Fatalf("blank final answer must error, got %v", err)
	}
}

func TestOpenAIAskUnknownToolRecovers(t *testing.T) {
	call := 0
	p := testOpenAIProvider(t, func(w http.ResponseWriter, r *http.Request) {
		call++
		w.Header().Set("Content-Type", "application/json")
		if call == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"nope","arguments":"{}"}}]}}]}`))
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		msgs, _ := req["messages"].([]any)
		last := msgs[len(msgs)-1].(map[string]any)
		if !strings.Contains(last["content"].(string), "unknown tool") {
			t.Errorf("model not told about unknown tool: %+v", last)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"Sorry, I cannot check that."}}]}`))
	})
	if _, err := p.ask(context.Background(), "sys", "q", nil); err != nil {
		t.Fatalf("ask should recover from unknown tool: %v", err)
	}
}

func TestOpenAIAskAPIError(t *testing.T) {
	p := testOpenAIProvider(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	})
	if _, err := p.ask(context.Background(), "sys", "q", nil); err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("api error must propagate with the server's message, got %v", err)
	}
}
