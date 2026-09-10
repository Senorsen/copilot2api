package anthropic

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/whtsky/copilot2api/internal/models"
	"github.com/whtsky/copilot2api/internal/reqctx"
	"github.com/whtsky/copilot2api/internal/upstream"
	"github.com/whtsky/copilot2api/stats"
)

func TestReadSSEEventMultiLineData(t *testing.T) {
	input := strings.Join([]string{
		"event: response.output_text.delta",
		"data: {\"type\":\"response.output_text.delta\",",
		"data: \"delta\":\"hello\"}",
		"",
	}, "\n")

	reader := bufio.NewReader(strings.NewReader(input))
	event, err := readSSEEvent(reader)
	if err != nil {
		t.Fatalf("readSSEEvent returned error: %v", err)
	}
	if event == nil {
		t.Fatal("readSSEEvent returned nil event")
	}

	if event.Event != "response.output_text.delta" {
		t.Fatalf("event type = %q, want %q", event.Event, "response.output_text.delta")
	}

	wantData := "{\"type\":\"response.output_text.delta\",\n\"delta\":\"hello\"}"
	if event.Data != wantData {
		t.Fatalf("event data = %q, want %q", event.Data, wantData)
	}
}

func TestReadSSEEventEOFWithoutData(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader(""))
	event, err := readSSEEvent(reader)
	if err == nil {
		t.Fatal("expected EOF error")
	}
	if err != io.EOF {
		t.Fatalf("error = %v, want io.EOF", err)
	}
	if event != nil {
		t.Fatalf("event = %#v, want nil", event)
	}
}

func TestRequestedReasoningEffort(t *testing.T) {
	tests := []struct {
		name string
		req  AnthropicMessagesRequest
		want string
	}{
		{name: "unspecified", want: "unspecified"},
		{
			name: "explicit effort is normalized",
			req:  AnthropicMessagesRequest{OutputConfig: &AnthropicOutputConfig{Effort: " XHIGH "}},
			want: "xhigh",
		},
		{
			name: "explicit effort overrides budget",
			req: AnthropicMessagesRequest{
				OutputConfig: &AnthropicOutputConfig{Effort: "max"},
				Thinking:     &AnthropicThinking{BudgetTokens: intPtr(4000)},
			},
			want: "max",
		},
		{
			name: "empty explicit effort falls back to budget",
			req: AnthropicMessagesRequest{
				OutputConfig: &AnthropicOutputConfig{},
				Thinking:     &AnthropicThinking{BudgetTokens: intPtr(8000)},
			},
			want: "medium",
		},
		{
			name: "thinking without budget is unspecified",
			req:  AnthropicMessagesRequest{Thinking: &AnthropicThinking{Type: "adaptive"}},
			want: "unspecified",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := requestedReasoningEffort(tt.req); got != tt.want {
				t.Fatalf("requestedReasoningEffort() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHandlerRecordsReasoningEffort(t *testing.T) {
	fakeUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{"id": "claude-test", "supported_endpoints": []string{"/v1/messages"}},
				},
			})
		case "/v1/messages":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"id":"msg_test",
				"type":"message",
				"role":"assistant",
				"model":"claude-test",
				"content":[{"type":"text","text":"ok"}],
				"stop_reason":"end_turn",
				"stop_sequence":null,
				"usage":{"input_tokens":10,"cache_read_input_tokens":2,"cache_creation_input_tokens":1,"output_tokens":5}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer fakeUpstream.Close()

	tp := &statsTestTokenProvider{
		baseURL:   fakeUpstream.URL,
		accountID: "acc-anthropic",
		username:  "alice",
	}
	upstreamClient := upstream.NewClient(tp, nil)
	modelsCache := models.NewCache(func() *upstream.Client { return upstreamClient }, time.Minute)
	statsDir := t.TempDir()
	recorder := stats.NewRecorder(statsDir)
	handler := NewHandler(tp, nil, modelsCache)
	handler.StatsRecorder = recorder

	body := `{
		"model":"claude-test",
		"max_tokens":16,
		"messages":[{"role":"user","content":"hello"}],
		"thinking":{"type":"enabled","budget_tokens":4000},
		"output_config":{"effort":"max"}
	}`
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	request = request.WithContext(reqctx.WithClientID(request.Context(), "client-anthropic"))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	recorder.Close()

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	paths, err := filepath.Glob(filepath.Join(statsDir, "acc-anthropic", "*", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 {
		t.Fatalf("stats files = %v, want one", paths)
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var entry stats.Entry
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("decode stats entry: %v", err)
	}
	if entry.ReasoningEffort != "max" {
		t.Errorf("ReasoningEffort = %q, want max", entry.ReasoningEffort)
	}
	if entry.ClientID != "client-anthropic" {
		t.Errorf("ClientID = %q, want client-anthropic", entry.ClientID)
	}
	if entry.AccountID != "acc-anthropic" || entry.Username != "alice" {
		t.Errorf("account = (%q, %q), want (acc-anthropic, alice)", entry.AccountID, entry.Username)
	}
	if entry.TokensIn != 13 || entry.TokensOut != 5 || entry.TokensTotal != 18 {
		t.Errorf("token usage = (%d, %d, %d), want (13, 5, 18)", entry.TokensIn, entry.TokensOut, entry.TokensTotal)
	}
}

type statsTestTokenProvider struct {
	baseURL   string
	accountID string
	username  string
}

func (p *statsTestTokenProvider) GetToken(context.Context) (string, error) {
	return "test-token", nil
}

func (p *statsTestTokenProvider) GetBaseURL() string {
	return p.baseURL
}

func (p *statsTestTokenProvider) GetAccountInfo() (string, string) {
	return p.accountID, p.username
}

func TestNormalizeNativeMessagesBody_RemovesCacheControlScope(t *testing.T) {
	body := []byte(`{
		"model": "claude-opus-4-6-20250514",
		"context_management": {"type": "auto"},
		"system": [
			{"type": "text", "text": "one"},
			{"type": "text", "text": "two", "cache_control": {"type": "ephemeral", "ttl": "1h", "scope": "workspace"}}
		],
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "hi", "cache_control": {"type": "ephemeral", "scope": "tool"}}]}
		],
		"max_tokens": 16
	}`)

	normalized, err := normalizeNativeMessagesBody(body, "claude-opus-4.6", true)
	if err != nil {
		t.Fatalf("normalizeNativeMessagesBody returned error: %v", err)
	}

	info := inspectCacheControl(normalized)
	if info.ScopeCount != 0 {
		t.Fatalf("ScopeCount = %d, want 0; paths=%v", info.ScopeCount, info.ScopePaths)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(normalized, &decoded); err != nil {
		t.Fatalf("failed to decode normalized body: %v", err)
	}

	if decoded["model"] != "claude-opus-4.6" {
		t.Fatalf("model = %v, want claude-opus-4.6", decoded["model"])
	}
	if _, ok := decoded["context_management"]; ok {
		t.Fatalf("context_management still present")
	}

	system := decoded["system"].([]interface{})
	cacheControl := system[1].(map[string]interface{})["cache_control"].(map[string]interface{})
	if cacheControl["type"] != "ephemeral" {
		t.Fatalf("system cache_control.type = %v, want ephemeral", cacheControl["type"])
	}
	if cacheControl["ttl"] != "1h" {
		t.Fatalf("system cache_control.ttl = %v, want 1h", cacheControl["ttl"])
	}
	if _, ok := cacheControl["scope"]; ok {
		t.Fatalf("system cache_control.scope still present")
	}

	messages := decoded["messages"].([]interface{})
	parts := messages[0].(map[string]interface{})["content"].([]interface{})
	messageCacheControl := parts[0].(map[string]interface{})["cache_control"].(map[string]interface{})
	if _, ok := messageCacheControl["scope"]; ok {
		t.Fatalf("message cache_control.scope still present")
	}
}

func TestConfiguredAliasesRouteAndPreserveClientModel(t *testing.T) {
	for _, endpoint := range []string{"/chat/completions", "/responses", "/v1/messages"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", endpoint, stream), func(t *testing.T) {
				var seenModel, seenVersion, seenBeta string
				fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/models" {
						_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "gpt-6-astra", "supported_endpoints": []string{endpoint}}}})
						return
					}
					if r.URL.Path != endpoint {
						t.Errorf("endpoint %s want %s", r.URL.Path, endpoint)
						http.NotFound(w, r)
						return
					}
					var body map[string]any
					_ = json.NewDecoder(r.Body).Decode(&body)
					seenModel, _ = body["model"].(string)
					seenVersion = r.Header.Get("anthropic-version")
					seenBeta = r.Header.Get("anthropic-beta")
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						switch endpoint {
						case "/chat/completions":
							fmt.Fprint(w, "data: {\"id\":\"test\",\"model\":\"gpt-6-astra\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"OK\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"test\",\"model\":\"gpt-6-astra\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
						case "/responses":
							fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_test\",\"model\":\"gpt-6-astra\"}}\n\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"model\":\"gpt-6-astra\",\"status\":\"completed\",\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}}\n\n")
						case "/v1/messages":
							fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_test\",\"type\":\"message\",\"model\":\"gpt-6-astra\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":5,\"output_tokens\":0}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
						}
						return
					}
					w.Header().Set("Content-Type", "application/json")
					switch endpoint {
					case "/chat/completions":
						fmt.Fprint(w, `{"id":"test","model":"gpt-6-astra","choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":1}}`)
					case "/responses":
						fmt.Fprint(w, `{"id":"resp_test","model":"gpt-6-astra","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"}]}],"usage":{"input_tokens":5,"output_tokens":1}}`)
					case "/v1/messages":
						fmt.Fprint(w, `{"id":"msg_test","type":"message","role":"assistant","model":"gpt-6-astra","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":1}}`)
					}
				}))
				defer fake.Close()
				tp := &statsTestTokenProvider{baseURL: fake.URL}
				mc := models.NewCacheWithAliases(func() *upstream.Client { return upstream.NewClient(tp, nil) }, time.Minute, models.Aliases{"claude-opus-5[1m]": "gpt-6-astra"})
				h := NewHandler(tp, nil, mc)
				body := fmt.Sprintf(`{"model":"claude-opus-5[1m]","max_tokens":16,"stream":%v,"messages":[{"role":"user","content":"hi"}]}`, stream)
				r := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
				r.Header.Set("anthropic-version", "2023-06-01")
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != 200 {
					t.Fatalf("%d %s", w.Code, w.Body.String())
				}
				if seenModel != "gpt-6-astra" || seenVersion != "2023-06-01" || strings.Contains(seenBeta, "context-1m") {
					t.Fatalf("model/version/beta %q %q %q", seenModel, seenVersion, seenBeta)
				}
				if !strings.Contains(w.Body.String(), `"model":"claude-opus-5[1m]"`) {
					t.Fatalf("client alias not preserved: %s", w.Body.String())
				}
				if w.Header().Get("X-Upstream-Model") != "gpt-6-astra" {
					t.Fatal("upstream transparency header missing")
				}
			})
		}
	}
}
