package counciltransport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func ptrString(value string) *string { return &value }

func ptrInt(value int) *int { return &value }

const loopbackCredential = "loopback-secret"

func validChatRequest() Request {
	return Request{Model: "deepseek-flash", Prompt: "hi", Effort: "medium"}
}

// Mirrors the PS chat envelope fixture: observed model from the body, chat
// usage vocabulary, terminal finish_reason stop.
const happyPathChatBody = `{"id":"chatcmpl-1","model":"deepseek-chat-resolved","usage":{"prompt_tokens":10,"completion_tokens":5},"choices":[{"message":{"role":"assistant","content":"{\"role\":\"intent_critic\",\"verdict\":\"PASS\"}"},"finish_reason":"stop"}]}`

func TestRequestCanonicalMirrorsLegacyWireBody(t *testing.T) {
	canonical, err := validChatRequest().Canonical()
	if err != nil {
		t.Fatalf("canonical request: %v", err)
	}
	expected := `{"model":"deepseek-flash","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_object"},"reasoning_effort":"medium"}`
	if string(canonical) != expected {
		t.Fatalf("canonical request body:\n got %s\nwant %s", canonical, expected)
	}

	integer, err := Request{Model: "deepseek-flash", Prompt: "hi", Effort: "100"}.Canonical()
	if err != nil {
		t.Fatalf("canonical integer effort request: %v", err)
	}
	if !strings.Contains(string(integer), `"max_tokens":100`) || strings.Contains(string(integer), "reasoning_effort") {
		t.Fatalf("integer effort must ride max_tokens only: %s", integer)
	}

	for _, effort := range []string{"", "bogus", "0", "-5"} {
		if _, err := (Request{Model: "m", Prompt: "hi", Effort: effort}).Canonical(); err == nil {
			t.Fatalf("effort %q must be rejected", effort)
		}
	}
	if _, err := (Request{Model: "  ", Prompt: "hi", Effort: "medium"}).Canonical(); err == nil {
		t.Fatal("empty model must be rejected")
	}
	if _, err := (Request{Model: "m", Prompt: " ", Effort: "medium"}).Canonical(); err == nil {
		t.Fatal("empty prompt must be rejected")
	}
}

func TestCredentialsNeverRenderTheKey(t *testing.T) {
	creds := Credentials{APIKey: "super-secret-token"}
	for _, rendered := range []string{
		creds.String(),
		fmt.Sprintf("%v", creds),
		fmt.Sprintf("%s", creds),
		fmt.Sprintf("%#v", creds),
	} {
		if strings.Contains(rendered, "super-secret-token") {
			t.Fatalf("credential leaked through formatting: %s", rendered)
		}
	}
	encoded, err := json.Marshal(struct{ Creds Credentials }{creds})
	if err != nil {
		t.Fatalf("marshal credentials: %v", err)
	}
	if strings.Contains(string(encoded), "super-secret-token") {
		t.Fatalf("credential leaked through JSON: %s", encoded)
	}
}

func TestChatHappyPathLoopback(t *testing.T) {
	var requests atomic.Int32
	var capturedMethod, capturedPath, capturedAuth, capturedBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		capturedMethod, capturedPath, capturedAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		capturedBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, happyPathChatBody)
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL + "/v1"}
	response, err := client.Chat(context.Background(), validChatRequest(), Credentials{APIKey: loopbackCredential})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("exactly one request expected, got %d", requests.Load())
	}
	if capturedMethod != http.MethodPost || capturedPath != "/v1/chat/completions" {
		t.Fatalf("unexpected wire request: %s %s", capturedMethod, capturedPath)
	}
	if capturedAuth != "Bearer "+loopbackCredential {
		t.Fatalf("credential must ride the Authorization header: %q", capturedAuth)
	}

	var wire map[string]any
	if err := json.Unmarshal([]byte(capturedBody), &wire); err != nil {
		t.Fatalf("wire body is not JSON: %v", err)
	}
	if wire["model"] != "deepseek-flash" {
		t.Fatalf("wire model: %v", wire["model"])
	}
	messages, ok := wire["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("wire messages: %v", wire["messages"])
	}
	message, _ := messages[0].(map[string]any)
	if message["role"] != "user" || message["content"] != "hi" {
		t.Fatalf("wire message: %v", messages[0])
	}
	responseFormat, _ := wire["response_format"].(map[string]any)
	if responseFormat["type"] != "json_object" {
		t.Fatalf("wire response_format: %v", wire["response_format"])
	}
	if wire["reasoning_effort"] != "medium" {
		t.Fatalf("wire reasoning_effort: %v", wire["reasoning_effort"])
	}
	for _, forbidden := range []string{"stream", "temperature", "max_tokens"} {
		if _, present := wire[forbidden]; present {
			t.Fatalf("wire body must not carry %s: %s", forbidden, capturedBody)
		}
	}

	if response.ObservedModel != "deepseek-chat-resolved" {
		t.Fatalf("observed model must come from the provider envelope, got %q", response.ObservedModel)
	}
	if response.Usage.PromptTokens != 10 || response.Usage.CompletionTokens != 5 || response.Usage.ReasoningTokens != 0 {
		t.Fatalf("usage mapping: %+v", response.Usage)
	}
	wantContent := `{"role":"intent_critic","verdict":"PASS"}`
	if response.Content != wantContent {
		t.Fatalf("content: got %q want %q", response.Content, wantContent)
	}
}

func TestChatRedirectRefusedAndNeverFollowed(t *testing.T) {
	var targetRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetRequests.Add(1)
		fmt.Fprint(w, "{}")
	}))
	defer target.Close()

	var sourceRequests atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceRequests.Add(1)
		http.Redirect(w, r, target.URL+"/v1/chat/completions", http.StatusFound)
	}))
	defer source.Close()

	client := Client{BaseURL: source.URL}
	_, err := client.Chat(context.Background(), validChatRequest(), Credentials{APIKey: loopbackCredential})
	if err == nil {
		t.Fatal("redirect must be refused")
	}
	var redirectError *RedirectError
	if !errors.As(err, &redirectError) {
		t.Fatalf("redirect must produce a RedirectError, got %v", err)
	}
	if !errors.Is(err, ErrRedirectRefused) {
		t.Fatalf("redirect error must unwrap ErrRedirectRefused: %v", err)
	}
	if redirectError.Status != http.StatusFound {
		t.Fatalf("redirect status: %d", redirectError.Status)
	}
	if redirectError.Location == "" || strings.Contains(redirectError.Location, loopbackCredential) {
		t.Fatalf("redirect location: %q", redirectError.Location)
	}
	if sourceRequests.Load() != 1 {
		t.Fatalf("source must see exactly one request, got %d", sourceRequests.Load())
	}
	if targetRequests.Load() != 0 {
		t.Fatalf("redirect target must never receive a credential-bearing request, got %d", targetRequests.Load())
	}
}

func TestChatTimeoutAfterSendIsUnknownAfterDispatch(t *testing.T) {
	var dispatched atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dispatched.Add(1)
		time.Sleep(400 * time.Millisecond)
		fmt.Fprint(w, "{}")
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	client := Client{BaseURL: server.URL}
	_, err := client.Chat(ctx, validChatRequest(), Credentials{APIKey: loopbackCredential})
	if !errors.Is(err, ErrUnknownAfterDispatch) {
		t.Fatalf("timeout after send must be unknown after dispatch, got %v", err)
	}
	if errors.Is(err, ErrBeforeDispatch) {
		t.Fatalf("timeout after send must not classify as before dispatch: %v", err)
	}
	if dispatched.Load() != 1 {
		t.Fatal("request must reach the provider before the deadline")
	}
	if strings.Contains(err.Error(), loopbackCredential) {
		t.Fatalf("timeout error leaked the credential: %v", err)
	}
}

func TestChatStalledBodyIsUnknownAfterDispatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		time.Sleep(400 * time.Millisecond)
		fmt.Fprint(w, "{}")
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	client := Client{BaseURL: server.URL}
	started := time.Now()
	_, err := client.Chat(ctx, validChatRequest(), Credentials{APIKey: loopbackCredential})
	elapsed := time.Since(started)
	if !errors.Is(err, ErrUnknownAfterDispatch) {
		t.Fatalf("stalled body must be unknown after dispatch, got %v", err)
	}
	if elapsed > 350*time.Millisecond {
		t.Fatalf("stalled body must respect the overall deadline, took %s", elapsed)
	}
}

func TestChatConnectionRefusedIsBeforeDispatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	baseURL := server.URL
	server.Close()

	client := Client{BaseURL: baseURL}
	_, err := client.Chat(context.Background(), validChatRequest(), Credentials{APIKey: loopbackCredential})
	if !errors.Is(err, ErrBeforeDispatch) {
		t.Fatalf("connection refused must be before dispatch, got %v", err)
	}
	if errors.Is(err, ErrUnknownAfterDispatch) {
		t.Fatalf("connection refused must not classify as unknown after dispatch: %v", err)
	}
}

func TestChatOversizeStreamedBodyIsBoundError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		fmt.Fprint(w, strings.Repeat("x", 4096))
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL, MaxResponseBytes: 128}
	_, err := client.Chat(context.Background(), validChatRequest(), Credentials{APIKey: loopbackCredential})
	if !errors.Is(err, ErrResponseBound) {
		t.Fatalf("oversize streamed body must be a bound error, got %v", err)
	}
}

func TestChatOversizeDeclaredBodyIsBoundError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No explicit flush: net/http buffers and declares Content-Length.
		fmt.Fprint(w, strings.Repeat("x", 4096))
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL, MaxResponseBytes: 128}
	_, err := client.Chat(context.Background(), validChatRequest(), Credentials{APIKey: loopbackCredential})
	if !errors.Is(err, ErrResponseBound) {
		t.Fatalf("oversize declared body must be a bound error, got %v", err)
	}
}

func TestChatProviderStatusNeverLeaksCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, "denied for key %s, Authorization: Bearer %s", loopbackCredential, loopbackCredential)
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL}
	_, err := client.Chat(context.Background(), validChatRequest(), Credentials{APIKey: loopbackCredential})
	var statusError *ProviderStatusError
	if !errors.As(err, &statusError) {
		t.Fatalf("non-2xx must produce a ProviderStatusError, got %v", err)
	}
	if statusError.Code != http.StatusUnauthorized {
		t.Fatalf("provider status code: %d", statusError.Code)
	}
	if strings.Contains(err.Error(), loopbackCredential) || strings.Contains(statusError.Excerpt, loopbackCredential) {
		t.Fatalf("provider status error leaked the credential: %v", err)
	}
	if !strings.Contains(statusError.Excerpt, "<redacted>") {
		t.Fatalf("excerpt must show redaction: %q", statusError.Excerpt)
	}
}

func TestChatServerErrorCodeCarriesStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, "err")
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL}
	_, err := client.Chat(context.Background(), validChatRequest(), Credentials{APIKey: loopbackCredential})
	var statusError *ProviderStatusError
	if !errors.As(err, &statusError) || statusError.Code != http.StatusInternalServerError {
		t.Fatalf("5xx must carry the status code, got %v", err)
	}
}

func TestChatInputBoundBlocksBeforeDispatch(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		fmt.Fprint(w, "{}")
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL, MaxRequestBytes: 64}
	_, err := client.Chat(context.Background(), Request{Model: "m", Prompt: strings.Repeat("p", 512), Effort: "medium"}, Credentials{APIKey: loopbackCredential})
	if !errors.Is(err, ErrBeforeDispatch) {
		t.Fatalf("input overflow must fail before dispatch, got %v", err)
	}
	if requests.Load() != 0 {
		t.Fatal("input overflow must open no HTTP connection")
	}
}

func TestChatValidatesConfigurationBeforeAnyNetwork(t *testing.T) {
	cases := []struct {
		name   string
		client Client
	}{
		{"timeout below floor", Client{BaseURL: "https://api.deepseek.com", Timeout: 59 * time.Second}},
		{"timeout above ceiling", Client{BaseURL: "https://api.deepseek.com", Timeout: 901 * time.Second}},
		{"max request bytes above ceiling", Client{BaseURL: "https://api.deepseek.com", MaxRequestBytes: MaxBodyBytesLimit + 1}},
		{"max request bytes negative", Client{BaseURL: "https://api.deepseek.com", MaxRequestBytes: -1}},
		{"max response bytes above ceiling", Client{BaseURL: "https://api.deepseek.com", MaxResponseBytes: MaxBodyBytesLimit + 1}},
		{"max response bytes negative", Client{BaseURL: "https://api.deepseek.com", MaxResponseBytes: -1}},
		{"plain http off loopback", Client{BaseURL: "http://example.com/v1"}},
		{"unsupported scheme", Client{BaseURL: "ftp://example.com/v1"}},
		{"userinfo in url", Client{BaseURL: "https://user:pw@example.com/v1"}},
		{"query in url", Client{BaseURL: "https://example.com/v1?x=1"}},
		{"fragment in url", Client{BaseURL: "https://example.com/v1#f"}},
		{"empty url", Client{BaseURL: "   "}},
	}
	for _, testCase := range cases {
		_, err := testCase.client.Chat(context.Background(), validChatRequest(), Credentials{APIKey: loopbackCredential})
		if !errors.Is(err, ErrBeforeDispatch) {
			t.Fatalf("%s: expected a before-dispatch failure, got %v", testCase.name, err)
		}
	}

	if _, err := (Client{BaseURL: "https://api.deepseek.com"}).Chat(context.Background(), validChatRequest(), Credentials{}); !errors.Is(err, ErrBeforeDispatch) {
		t.Fatalf("empty credential must fail before dispatch, got %v", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (Client{BaseURL: "https://api.deepseek.com"}).Chat(cancelled, validChatRequest(), Credentials{APIKey: loopbackCredential}); !errors.Is(err, ErrBeforeDispatch) {
		t.Fatalf("pre-dispatch cancellation must fail before dispatch, got %v", err)
	}

	if _, err := (Client{BaseURL: "https://api.deepseek.com"}).Chat(context.Background(), Request{Model: "m", Prompt: "hi", Effort: "wat"}, Credentials{APIKey: loopbackCredential}); !errors.Is(err, ErrBeforeDispatch) {
		t.Fatalf("invalid effort must fail before dispatch, got %v", err)
	}
}

func TestParseResponseUsageVocabulary(t *testing.T) {
	cases := []struct {
		name string
		body string
		want Usage
	}{
		{"chat vocabulary", `{"model":"m","choices":[{"message":{"content":"x"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`, Usage{10, 5, 0}},
		{"openai vocabulary", `{"model":"m","choices":[{"message":{"content":"x"},"finish_reason":"stop"}],"usage":{"input_tokens":7,"output_tokens":3}}`, Usage{7, 3, 0}},
		{"reasoning variant nested", `{"model":"m","choices":[{"message":{"content":"x"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"completion_tokens_details":{"reasoning_tokens":4}}}`, Usage{1, 2, 4}},
		{"reasoning variant top level", `{"model":"m","choices":[{"message":{"content":"x"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"reasoning_tokens":9}}`, Usage{1, 2, 9}},
		{"input vocabulary wins", `{"model":"m","choices":[{"message":{"content":"x"},"finish_reason":"stop"}],"usage":{"input_tokens":9,"prompt_tokens":1,"output_tokens":8,"completion_tokens":2}}`, Usage{9, 8, 0}},
		{"unknown usage fields tolerated", `{"model":"m","choices":[{"message":{"content":"x"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":6,"cached_tokens":100,"prompt_cache_hit_tokens":"yes"}}`, Usage{4, 6, 0}},
		{"absent usage tolerated", `{"model":"m","choices":[{"message":{"content":"x"},"finish_reason":"stop"}]}`, Usage{}},
	}
	for _, testCase := range cases {
		response, err := ParseResponse([]byte(testCase.body))
		if err != nil {
			t.Fatalf("%s: parse: %v", testCase.name, err)
		}
		if response.Usage != testCase.want {
			t.Fatalf("%s: usage mapping: got %+v want %+v", testCase.name, response.Usage, testCase.want)
		}
	}

	malformed, err := ParseResponse([]byte(`{"model":"m","choices":[{"message":{"content":"x"},"finish_reason":"stop"}],"usage":{"prompt_tokens":"ten"}}`))
	if err == nil || !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("non-numeric usage must be an invalid response, got %v (%+v)", err, malformed)
	}
}

func TestParseResponseStrictEnvelope(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"empty choices", `{"id":"x","model":"m","choices":[]}`},
		{"no choices key", `{"id":"x","model":"m"}`},
		{"multiple choices", `{"model":"m","choices":[{"message":{"content":"a"},"finish_reason":"stop"},{"message":{"content":"b"},"finish_reason":"stop"}]}`},
		{"non terminal finish reason", `{"model":"m","choices":[{"message":{"content":"x"},"finish_reason":"length"}]}`},
		{"missing finish reason", `{"model":"m","choices":[{"message":{"content":"x"}}]}`},
		{"blank content", `{"model":"m","choices":[{"message":{"content":"   "},"finish_reason":"stop"}]}`},
		{"content parts without text", `{"model":"m","choices":[{"message":{"content":[{"type":"image_url"}]},"finish_reason":"stop"}]}`},
		{"bare role payload without envelope", `{"role":"intent_critic","verdict":"PASS"}`},
		{"two json objects", `{"a":1} {"b":2}`},
		{"malformed json", `{broken`},
		{"fenced payload", "```json\n{\"model\":\"m\"}\n```"},
		{"empty body", ``},
	}
	for _, testCase := range cases {
		if _, err := ParseResponse([]byte(testCase.body)); err == nil || !errors.Is(err, ErrInvalidResponse) {
			t.Fatalf("%s: expected an invalid response error, got %v", testCase.name, err)
		}
	}

	parts, err := ParseResponse([]byte(`{"model":"m","choices":[{"message":{"content":[{"text":"a"},{"type":"ref"},{"text":"b"}]},"finish_reason":"stop"}]}`))
	if err != nil {
		t.Fatalf("content parts: %v", err)
	}
	if parts.Content != "a\nb" {
		t.Fatalf("content parts join: %q", parts.Content)
	}

	withoutModel, err := ParseResponse([]byte(`{"choices":[{"message":{"content":"x"},"finish_reason":"stop"}]}`))
	if err != nil {
		t.Fatalf("absent model: %v", err)
	}
	if withoutModel.ObservedModel != "" {
		t.Fatalf("absent model must yield an empty observation, got %q", withoutModel.ObservedModel)
	}
	nonStringModel, err := ParseResponse([]byte(`{"model":17,"choices":[{"message":{"content":"x"},"finish_reason":"stop"}]}`))
	if err != nil {
		t.Fatalf("non-string model: %v", err)
	}
	if nonStringModel.ObservedModel != "" {
		t.Fatalf("non-string model must be tolerated as empty, got %q", nonStringModel.ObservedModel)
	}
}

func validMember() Member {
	return Member{
		Role:            "intent_critic",
		AttemptID:       "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee",
		Status:          "completed",
		Summary:         "member completed",
		Requested:       Identity{Provider: "deepseek", Model: "deepseek-flash", Effort: "medium"},
		Observed:        Observed{Provider: ptrString("deepseek"), Model: ptrString("deepseek-chat-resolved"), Effort: ptrString("medium")},
		ExecutionMode:   "direct_api",
		InputHashes:     InputHashes{OriginalTaskSHA256: strings.Repeat("a", 64), SpecSHA256: ptrString(strings.Repeat("b", 64))},
		PayloadSHA256:   ptrString(strings.Repeat("c", 64)),
		Usage:           MemberUsageFrom(Usage{PromptTokens: 10, CompletionTokens: 5, ReasoningTokens: 2}),
		DispatchedAtUTC: ptrString("2026-09-12T10:00:00Z"),
		CompletedAtUTC:  ptrString("2026-09-12T10:00:05Z"),
	}
}

func TestMemberCanonicalDeterministicAndClosedFieldSet(t *testing.T) {
	first, err := validMember().Canonical()
	if err != nil {
		t.Fatalf("member canonical: %v", err)
	}
	second, err := validMember().Canonical()
	if err != nil {
		t.Fatalf("member canonical: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("member canonical bytes must be deterministic:\n%s\n%s", first, second)
	}

	var decoded map[string]any
	if err := json.Unmarshal(first, &decoded); err != nil {
		t.Fatalf("member canonical is not JSON: %v", err)
	}
	wantFields := map[string]bool{
		"schema_version": true, "role": true, "attempt_id": true, "status": true,
		"summary": true, "requested": true, "observed": true, "execution_mode": true,
		"fallback_reason": true, "input_hashes": true, "payload_sha256": true,
		"usage": true, "cost_state": true, "dispatched_at_utc": true, "completed_at_utc": true,
	}
	if len(decoded) != len(wantFields) {
		t.Fatalf("member canonical field set: %v", decoded)
	}
	for field := range wantFields {
		if _, present := decoded[field]; !present {
			t.Fatalf("member canonical misses field %s: %s", field, first)
		}
	}
	if decoded["schema_version"] != float64(1) {
		t.Fatalf("member schema_version: %v", decoded["schema_version"])
	}
	if decoded["cost_state"] != CostStateProviderUsageReported {
		t.Fatalf("completed member with usage must derive %s, got %v", CostStateProviderUsageReported, decoded["cost_state"])
	}
	usage, _ := decoded["usage"].(map[string]any)
	if usage == nil || usage["input_tokens"] != float64(10) || usage["output_tokens"] != float64(5) || usage["reasoning_tokens"] != float64(2) {
		t.Fatalf("member usage projection: %v", decoded["usage"])
	}
}

func TestMemberCostStateClosedSet(t *testing.T) {
	unknownState := validMember()
	unknownState.CostState = "bogus"
	if _, err := unknownState.Canonical(); err == nil || !strings.Contains(err.Error(), "cost_state") {
		t.Fatalf("unknown cost_state must be rejected, got %v", err)
	}

	failed := validMember()
	failed.Status = "failed_before_acceptance"
	failed.Usage = nil
	failed.PayloadSHA256 = nil
	canonical, err := failed.Canonical()
	if err != nil {
		t.Fatalf("failed member canonical: %v", err)
	}
	if !strings.Contains(string(canonical), `"cost_state":"unknown"`) || !strings.Contains(string(canonical), `"usage":null`) {
		t.Fatalf("failed member must resolve cost_state unknown with null usage: %s", canonical)
	}

	completedWithoutUsage := validMember()
	completedWithoutUsage.Usage = nil
	canonical, err = completedWithoutUsage.Canonical()
	if err != nil {
		t.Fatalf("completed member without usage canonical: %v", err)
	}
	if !strings.Contains(string(canonical), `"cost_state":"no_usage_reported"`) {
		t.Fatalf("completed member without usage must resolve no_usage_reported: %s", canonical)
	}

	explicit := validMember()
	explicit.CostState = CostStateNoUsageReported
	canonical, err = explicit.Canonical()
	if err != nil {
		t.Fatalf("explicit cost state canonical: %v", err)
	}
	if !strings.Contains(string(canonical), `"cost_state":"no_usage_reported"`) {
		t.Fatalf("explicit cost state must be preserved: %s", canonical)
	}
}

func TestMemberRejectsUnknownEnumValuesAndBadHashes(t *testing.T) {
	cases := map[string]func(*Member){
		"unknown role":           func(m *Member) { m.Role = "auditor" },
		"unknown status":         func(m *Member) { m.Status = "partially_completed" },
		"unknown execution mode": func(m *Member) { m.ExecutionMode = "opencode" },
		"schema version":         func(m *Member) { m.SchemaVersion = 2 },
		"empty attempt id":       func(m *Member) { m.AttemptID = " " },
		"empty summary":          func(m *Member) { m.Summary = "" },
		"empty requested model":  func(m *Member) { m.Requested.Model = "" },
		"bad input hash":         func(m *Member) { m.InputHashes.OriginalTaskSHA256 = "zz" },
		"bad spec hash":          func(m *Member) { m.InputHashes.SpecSHA256 = ptrString("nope") },
		"bad payload hash":       func(m *Member) { m.PayloadSHA256 = ptrString("nope") },
	}
	for name, mutate := range cases {
		member := validMember()
		mutate(&member)
		if _, err := member.Canonical(); err == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}
}
