package stagehost

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bsl-flow/cli/internal/councilengine"
	"bsl-flow/cli/internal/counciltransport"
)

// council_test.go verifies the direct API council dispatcher against an
// httptest loopback server (the W7 acceptance shape of counciltransport's
// loopback tests): the binding endpoint, the request body, the payload
// extraction and the observed/usage projection.

func councilTestBinding(t *testing.T, baseURL string) *councilengine.Binding {
	t.Helper()
	endpoint, err := councilengine.ParseEndpointURL(baseURL, "test")
	if err != nil {
		t.Fatalf("endpoint: %v", err)
	}
	return &councilengine.Binding{
		Provider:                   "deepseek",
		Model:                      "deepseek-flash",
		Effort:                     "medium",
		Protocol:                   "openai_compatible",
		Endpoint:                   endpoint,
		TransportCapabilityVersion: 1,
		PromptVersion:              "council-prompt-v2",
		MemberSchemaVersion:        1,
		InputHashes:                nil,
	}
}

func TestCouncilDirectAPIDispatch(t *testing.T) {
	var gotPath, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		data, _ := io.ReadAll(r.Body)
		gotBody = string(data)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"deepseek-chat-resolved","usage":{"prompt_tokens":10,"completion_tokens":5},"choices":[{"message":{"role":"assistant","content":"{\"role\":\"intent_critic\",\"verdict\":\"PASS\",\"findings\":[],\"do_not_change\":[]}"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	binding := councilTestBinding(t, server.URL)
	dispatcher := &councilDispatcher{
		runRoot: t.TempDir(),
		clientFor: func(b *councilengine.Binding, timeout int) counciltransport.Client {
			return counciltransport.Client{BaseURL: server.URL, Timeout: 0}
		},
	}
	attempt := &councilengine.Attempt{AttemptID: strings.Repeat("1", 32), Sequence: 1, Role: "intent_critic", Binding: binding}
	route := &councilengine.Route{Route: "direct_api", Credential: "test-token"}
	result, err := dispatcher.directAPIDispatch(context.Background(), attempt, "prompt text", route, 300)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("path: %s", gotPath)
	}
	if !strings.Contains(gotBody, `"model":"deepseek-flash"`) || !strings.Contains(gotBody, `"reasoning_effort":"medium"`) {
		t.Fatalf("request body: %s", gotBody)
	}
	if result.Status != "completed" {
		t.Fatalf("status: %s", result.Status)
	}
	payload, _ := councilengine.AsOrdered(result.Payload)
	if payload == nil || payload.Get("verdict") != "PASS" {
		t.Fatalf("payload: %v", payload)
	}
	if result.Observed.Model == nil || *result.Observed.Model != "deepseek-chat-resolved" {
		t.Fatalf("observed model: %v", result.Observed.Model)
	}
	if result.Usage == nil || *result.Usage.InputTokens != 10 {
		t.Fatalf("usage: %v", result.Usage)
	}
}

func TestCouncilDirectAPIDispatchServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	binding := councilTestBinding(t, server.URL)
	dispatcher := &councilDispatcher{
		runRoot: t.TempDir(),
		clientFor: func(b *councilengine.Binding, timeout int) counciltransport.Client {
			return counciltransport.Client{BaseURL: server.URL, Timeout: 0}
		},
	}
	attempt := &councilengine.Attempt{AttemptID: strings.Repeat("1", 32), Sequence: 1, Role: "intent_critic", Binding: binding}
	route := &councilengine.Route{Route: "direct_api", Credential: "test-token"}
	_, err := dispatcher.directAPIDispatch(context.Background(), attempt, "prompt", route, 300)
	if err == nil {
		t.Fatal("server error must be classified")
	}
	if !strings.Contains(err.Error(), "BF_UNKNOWN_AFTER_DISPATCH") {
		t.Fatalf("server error must be unknown-after-dispatch: %s", err)
	}
}

func TestCouncilDirectAPIDispatchMalformed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"not json"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	binding := councilTestBinding(t, server.URL)
	dispatcher := &councilDispatcher{
		runRoot: t.TempDir(),
		clientFor: func(b *councilengine.Binding, timeout int) counciltransport.Client {
			return counciltransport.Client{BaseURL: server.URL, Timeout: 0}
		},
	}
	attempt := &councilengine.Attempt{AttemptID: strings.Repeat("1", 32), Sequence: 1, Role: "intent_critic", Binding: binding}
	route := &councilengine.Route{Route: "direct_api", Credential: "test-token"}
	_, err := dispatcher.directAPIDispatch(context.Background(), attempt, "prompt", route, 300)
	if err == nil {
		t.Fatal("malformed payload must fail")
	}
	if !strings.Contains(err.Error(), "BF_INVALID_RESPONSE") {
		t.Fatalf("malformed payload must be invalid-response: %s", err)
	}
}
