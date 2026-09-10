package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/whtsky/copilot2api/auth"
	"github.com/whtsky/copilot2api/internal/models"
	"github.com/whtsky/copilot2api/storage"
)

type testGatewayAccount struct {
	id       string
	username string
	baseURL  string
}

func newGatewayAccountManager(t *testing.T, accounts ...testGatewayAccount) *auth.AccountManager {
	t.Helper()
	backend, err := storage.NewFileBackend(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range accounts {
		copilotToken, err := json.Marshal(auth.CopilotToken{
			Token:     "test-token",
			ExpiresAt: time.Now().Add(time.Hour),
			BaseURL:   account.baseURL,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := backend.SaveCredentials(context.Background(), account.id, &storage.Credentials{
			GitHubUsername: account.username,
			CopilotToken:   copilotToken,
		}); err != nil {
			t.Fatal(err)
		}
	}
	manager, err := auth.NewAccountManager(backend)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func writeModels(w http.ResponseWriter, models ...string) {
	data := make([]map[string]any, 0, len(models))
	for _, model := range models {
		data = append(data, map[string]any{"id": model, "supported_endpoints": []string{"/chat/completions"}})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func writeChatSuccess(w http.ResponseWriter, model string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"id":"chatcmpl-ok","object":"chat.completion","model":"` + model + `","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
}

func writeModelUnsupported(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_, _ = w.Write([]byte(`{"error":{"message":"The requested model is not supported.","code":"invalid_request_body"}}`))
}

func TestGatewayFiltersPoolByAccountModelAvailability(t *testing.T) {
	const requestedModel = "gpt-account-specific"
	var unsupportedInferenceCalls atomic.Int32
	unsupported := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			writeModels(w, "some-other-model")
		case "/chat/completions":
			unsupportedInferenceCalls.Add(1)
			writeModelUnsupported(w)
		default:
			http.NotFound(w, r)
		}
	}))
	defer unsupported.Close()

	var supportedInferenceCalls atomic.Int32
	supported := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			writeModels(w, requestedModel)
		case "/chat/completions":
			supportedInferenceCalls.Add(1)
			writeChatSuccess(w, requestedModel)
		default:
			http.NotFound(w, r)
		}
	}))
	defer supported.Close()

	handler := NewHandler(newGatewayAccountManager(t,
		testGatewayAccount{id: "unsupported", username: "unsupported", baseURL: unsupported.URL},
		testGatewayAccount{id: "supported", username: "supported", baseURL: supported.URL},
	), nil, nil)

	request := httptest.NewRequest(http.MethodPost, "/gw/api/v1/chat/completions", strings.NewReader(`{"model":"`+requestedModel+`","messages":[],"stream":false}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	if got := unsupportedInferenceCalls.Load(); got != 0 {
		t.Fatalf("unsupported account inference calls = %d, want 0", got)
	}
	if got := supportedInferenceCalls.Load(); got != 1 {
		t.Fatalf("supported account inference calls = %d, want 1", got)
	}
}

func TestGatewayRetriesModelUnsupportedOnAnotherAvailableAccount(t *testing.T) {
	const requestedModel = "gpt-transient-availability"
	var rejectedCalls atomic.Int32
	var rejectedModelsCalls atomic.Int32
	rejected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			rejectedModelsCalls.Add(1)
			writeModels(w, requestedModel)
		case "/chat/completions":
			rejectedCalls.Add(1)
			writeModelUnsupported(w)
		default:
			http.NotFound(w, r)
		}
	}))
	defer rejected.Close()

	var successfulCalls atomic.Int32
	successful := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			writeModels(w, requestedModel)
		case "/chat/completions":
			successfulCalls.Add(1)
			writeChatSuccess(w, requestedModel)
		default:
			http.NotFound(w, r)
		}
	}))
	defer successful.Close()

	handler := NewHandler(newGatewayAccountManager(t,
		testGatewayAccount{id: "rejected", username: "rejected", baseURL: rejected.URL},
		testGatewayAccount{id: "successful", username: "successful", baseURL: successful.URL},
	), nil, nil)
	handler.mu.Lock()
	handler.affinity["test-client|"+requestedModel] = &affinityEntry{AccountID: "rejected", ExpiresAt: time.Now().Add(time.Hour)}
	handler.mu.Unlock()

	request := httptest.NewRequest(http.MethodPost, "/gw/api/v1/chat/completions", strings.NewReader(`{"model":"`+requestedModel+`","messages":[],"stream":false}`))
	request.Header.Set("X-Real-IP", "test-client")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	if got := rejectedCalls.Load(); got != 1 {
		t.Fatalf("rejected account calls = %d, want 1", got)
	}
	if got := rejectedModelsCalls.Load(); got != 2 {
		t.Fatalf("rejected account model-list calls = %d, want 2 (initial + refresh after rejection)", got)
	}
	if got := successfulCalls.Load(); got != 1 {
		t.Fatalf("successful account calls = %d, want 1", got)
	}
	handler.mu.RLock()
	gotAffinity := handler.affinity["test-client|"+requestedModel].AccountID
	handler.mu.RUnlock()
	if gotAffinity != "successful" {
		t.Fatalf("affinity account = %q, want successful", gotAffinity)
	}
}

func TestGatewayRetriesStreamingModelUnsupported(t *testing.T) {
	const requestedModel = "gpt-stream-fallback"
	rejected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			writeModels(w, requestedModel)
			return
		}
		writeModelUnsupported(w)
	}))
	defer rejected.Close()

	successful := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			writeModels(w, requestedModel)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-ok\",\"object\":\"chat.completion.chunk\",\"model\":\"" + requestedModel + "\",\"choices\":[]}\n\ndata: [DONE]\n\n"))
	}))
	defer successful.Close()

	handler := NewHandler(newGatewayAccountManager(t,
		testGatewayAccount{id: "rejected", username: "rejected", baseURL: rejected.URL},
		testGatewayAccount{id: "successful", username: "successful", baseURL: successful.URL},
	), nil, nil)
	handler.mu.Lock()
	handler.affinity["stream-client|"+requestedModel] = &affinityEntry{AccountID: "rejected", ExpiresAt: time.Now().Add(time.Hour)}
	handler.mu.Unlock()

	request := httptest.NewRequest(http.MethodPost, "/gw/api/v1/chat/completions", strings.NewReader(`{"model":"`+requestedModel+`","messages":[],"stream":true}`))
	request.Header.Set("X-Real-IP", "stream-client")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "data: [DONE]") {
		t.Fatalf("stream body missing DONE: %s", response.Body.String())
	}
}

func TestGatewayConfiguredAliasUsesRealAccountModel(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "gpt-6-astra", "supported_endpoints": []string{"/chat/completions"}}}})
		case "/chat/completions":
			calls.Add(1)
			var v map[string]any
			_ = json.NewDecoder(r.Body).Decode(&v)
			if v["model"] != "gpt-6-astra" {
				t.Errorf("model %v", v["model"])
			}
			writeChatSuccess(w, "gpt-6-astra")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	mc := models.NewCacheWithAliases(nil, time.Minute, models.Aliases{"claude-opus-5[1m]": "gpt-6-astra"})
	handler := NewHandler(newGatewayAccountManager(t, testGatewayAccount{id: "mapped", username: "mapped", baseURL: server.URL}), nil, mc)
	r := httptest.NewRequest("POST", "/gw/api/v1/messages", strings.NewReader(`{"model":"claude-opus-5[1m]","max_tokens":16,"messages":[{"role":"user","content":"hi"}],"stream":false}`))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 200 || calls.Load() != 1 || !strings.Contains(w.Body.String(), `"model":"claude-opus-5[1m]"`) {
		t.Fatalf("code %d calls %d body %s", w.Code, calls.Load(), w.Body)
	}
}
