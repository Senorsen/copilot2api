package anthropic

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Responses usually supplies input/cache usage only in its terminal event.
// Check serialized events: testing the converter or server's separate usage
// recorder alone misses fields discarded by the Anthropic stream adapter.
func TestResponsesStreamFinalUsageOnWire(t *testing.T) {
	for _, terminal := range []string{"response.completed", "response.incomplete"} {
		for _, tc := range []struct {
			name           string
			input, cached  int
			output         int
			includeDetails bool
		}{
			{"incident_partial_cache", 922192, 921497, 400, true},
			{"noncached", 1234, 0, 56, false},
			{"fully_cached", 800000, 800000, 10, true},
			{"known_zero", 0, 0, 0, true},
		} {
			t.Run(terminal+"/"+tc.name, func(t *testing.T) {
				state := NewResponsesStreamState()
				start := TranslateResponsesStreamEvent(ResponseStreamEvent{
					Type:     "response.created",
					Response: &ResponsesResult{ID: "resp_usage", Model: "gpt-6-astra"},
				}, state)
				if len(start) != 1 || start[0].Message == nil {
					t.Fatalf("unexpected start: %+v", start)
				}
				initial := start[0].Message.Usage
				if initial.InputTokens != 0 || initial.CacheReadInputTokens != 0 {
					t.Fatalf("fixture must begin with unknown/zero input usage: %+v", initial)
				}
				TranslateResponsesStreamEvent(ResponseStreamEvent{
					Type: "response.output_text.delta", Delta: "OK", OutputIndex: 0, ContentIndex: 0,
				}, state)
				usage := &ResponsesUsage{InputTokens: tc.input, OutputTokens: tc.output}
				if tc.includeDetails {
					usage.InputTokensDetails = &InputTokenDetails{CachedTokens: tc.cached}
				}
				resp := &ResponsesResult{ID: "resp_usage", Model: "gpt-6-astra", Status: "completed", Usage: usage}
				if terminal == "response.incomplete" {
					resp.Status = "incomplete"
					resp.IncompleteDetails = &IncompleteDetails{Reason: "max_output_tokens"}
				}
				events := TranslateResponsesStreamEvent(ResponseStreamEvent{Type: terminal, Response: resp}, state)
				if len(events) != 3 || events[0].Type != "content_block_stop" || events[1].Type != "message_delta" || events[2].Type != "message_stop" {
					t.Fatalf("unexpected terminal event ordering: %+v", events)
				}
				wire := usageFieldsFromWire(t, events[1])
				want := map[string]int{
					"input_tokens":            tc.input - tc.cached,
					"cache_read_input_tokens": tc.cached,
					"output_tokens":           tc.output,
				}
				for key, value := range want {
					got, present := wire[key]
					if !present || got != value {
						t.Errorf("wire usage %s = %d (present=%v), want %d; fields=%v", key, got, present, value, wire)
					}
				}
				// Mirror Claude Code's positive input/cache updates; absent final
				// fields leave message_start's zeros in place, breaking compaction.
				merged := mergeClientInputUsage(initial, wire)
				context := merged.InputTokens + merged.CacheReadInputTokens + merged.CacheCreationInputTokens + merged.OutputTokens
				if context != tc.input+tc.output {
					t.Errorf("client context = %d, want %d (cached input must count exactly once)", context, tc.input+tc.output)
				}
				if tc.name == "incident_partial_cache" && context < 800000 {
					t.Errorf("client context %d would still miss the user's 800k compaction threshold", context)
				}
			})
		}
	}
}

func TestResponsesStreamMissingFinalUsagePreservesInitialInput(t *testing.T) {
	for _, nilResponse := range []bool{false, true} {
		name := "response_without_usage"
		if nilResponse {
			name = "nil_response"
		}
		t.Run(name, func(t *testing.T) {
			state := NewResponsesStreamState()
			start := TranslateResponsesStreamEvent(ResponseStreamEvent{
				Type: "response.created",
				Response: &ResponsesResult{ID: "resp_usage", Usage: &ResponsesUsage{
					InputTokens: 1000, InputTokensDetails: &InputTokenDetails{CachedTokens: 900},
				}},
			}, state)
			var response *ResponsesResult
			if !nilResponse {
				response = &ResponsesResult{ID: "resp_usage", Status: "completed"}
			}
			events := TranslateResponsesStreamEvent(ResponseStreamEvent{Type: "response.completed", Response: response}, state)
			wire := usageFieldsFromWire(t, events[0])
			for _, key := range []string{"input_tokens", "cache_read_input_tokens", "cache_creation_input_tokens"} {
				if _, present := wire[key]; present {
					t.Errorf("unknown final %s must be absent, not fabricated as zero", key)
				}
			}
			merged := mergeClientInputUsage(start[0].Message.Usage, wire)
			if merged.InputTokens != 100 || merged.CacheReadInputTokens != 900 {
				t.Errorf("lost initial usage: %+v", merged)
			}
		})
	}
}

func TestMessageDeltaOutputOnlyWireCompatibility(t *testing.T) {
	// Other adapters using this shared type must retain their current wire
	// shape unless they explicitly opt into the new final input fields.
	data, err := json.Marshal(&AnthropicMessageDeltaUsage{OutputTokens: 17})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"output_tokens":17}` {
		t.Fatalf("output-only usage changed: %s", data)
	}
}

func usageFieldsFromWire(t *testing.T, event AnthropicStreamEvent) map[string]int {
	t.Helper()
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Type  string         `json:"type"`
		Usage map[string]int `json:"usage"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Type != "message_delta" {
		t.Fatalf("expected message_delta, got %s", data)
	}
	return wire.Usage
}

func mergeClientInputUsage(initial AnthropicUsage, fields map[string]int) AnthropicUsage {
	if fields["input_tokens"] > 0 {
		initial.InputTokens = fields["input_tokens"]
	}
	if fields["cache_read_input_tokens"] > 0 {
		initial.CacheReadInputTokens = fields["cache_read_input_tokens"]
	}
	if fields["cache_creation_input_tokens"] > 0 {
		initial.CacheCreationInputTokens = fields["cache_creation_input_tokens"]
	}
	if output, present := fields["output_tokens"]; present {
		initial.OutputTokens = output
	}
	return initial
}

// Exercise the actual HTTP/SSE path as well as the event translator, and show
// that the server's separately recorded totals match what the client receives.
func TestResponsesHandlerSSEUsageMatchesServerAccounting(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []string{
			`{"type":"response.created","response":{"id":"resp_usage","model":"gpt-6-astra","status":"in_progress"}}`,
			`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"OK"}`,
			`{"type":"response.completed","response":{"id":"resp_usage","model":"gpt-6-astra","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"OK"}]}],"usage":{"input_tokens":922192,"input_tokens_details":{"cached_tokens":921497},"output_tokens":400,"total_tokens":922592}}}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", event)
		}
	}))
	defer fake.Close()
	handler := NewHandler(&statsTestTokenProvider{baseURL: fake.URL}, nil, nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{}`))
	response := httptest.NewRecorder()
	var usage tokenUsage
	handler.handleResponsesStreaming(response, request, ResponsesRequest{Model: "gpt-6-astra", Stream: true}, "gpt-6-astra[1m]", &usage)
	if response.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", response.Code, response.Body.String())
	}
	reader := bufio.NewReader(strings.NewReader(response.Body.String()))
	var merged AnthropicUsage
	var types []string
	for {
		event, err := readSSEEvent(reader)
		if err != nil || event == nil {
			break
		}
		var part struct {
			Type    string                     `json:"type"`
			Message *AnthropicMessagesResponse `json:"message"`
			Usage   map[string]int             `json:"usage"`
		}
		if err := json.Unmarshal([]byte(event.Data), &part); err != nil {
			t.Fatal(err)
		}
		types = append(types, part.Type)
		if part.Type == "message_start" {
			merged = part.Message.Usage
			if part.Message.Model != "gpt-6-astra[1m]" {
				t.Errorf("client model echo changed: %q", part.Message.Model)
			}
		}
		if part.Type == "message_delta" {
			merged = mergeClientInputUsage(merged, part.Usage)
		}
	}
	if len(types) == 0 || types[0] != "message_start" || types[len(types)-1] != "message_stop" {
		t.Fatalf("SSE lifecycle incomplete: %v", types)
	}
	if usage.In != 695 || usage.Cached != 921497 || usage.Out != 400 {
		t.Fatalf("unexpected server accounting: %+v", usage)
	}
	if merged.InputTokens != usage.In || merged.CacheReadInputTokens != usage.Cached || merged.OutputTokens != usage.Out {
		t.Fatalf("client usage %+v disagrees with server %+v", merged, usage)
	}
}
