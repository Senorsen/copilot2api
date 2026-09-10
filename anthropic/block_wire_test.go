package anthropic

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestBlockWireRequiredEmptyFields(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"text", AnthropicContentBlock{Type: "text"}, `{"type":"text","text":""}`},
		{"thinking", AnthropicContentBlock{Type: "thinking"}, `{"type":"thinking","thinking":"","signature":""}`},
		{"empty tool input", AnthropicContentBlock{Type: "tool_use", ID: "t1", Name: "probe", Input: map[string]interface{}{}}, `{"type":"tool_use","id":"t1","name":"probe","input":{}}`},
		{"nil tool input", AnthropicContentBlock{Type: "tool_use", ID: "t1", Name: "probe"}, `{"type":"tool_use","id":"t1","name":"probe","input":{}}`},
		{"text delta", AnthropicContentDelta{Type: "text_delta"}, `{"type":"text_delta","text":""}`},
		{"thinking delta", AnthropicContentDelta{Type: "thinking_delta"}, `{"type":"thinking_delta","thinking":""}`},
		{"signature delta", AnthropicContentDelta{Type: "signature_delta"}, `{"type":"signature_delta","signature":""}`},
		{"input delta", AnthropicContentDelta{Type: "input_json_delta"}, `{"type":"input_json_delta","partial_json":""}`},
		{"nonempty text delta", AnthropicContentDelta{Type: "text_delta", Text: "answer"}, `{"type":"text_delta","text":"answer"}`},
		{"nonempty thinking delta", AnthropicContentDelta{Type: "thinking_delta", Thinking: "summary"}, `{"type":"thinking_delta","thinking":"summary"}`},
		{"nonempty signature delta", AnthropicContentDelta{Type: "signature_delta", Signature: "opaque"}, `{"type":"signature_delta","signature":"opaque"}`},
		{"nonempty input delta", AnthropicContentDelta{Type: "input_json_delta", PartialJSON: "{}"}, `{"type":"input_json_delta","partial_json":"{}"}`},
		{"nonempty thinking", AnthropicContentBlock{Type: "thinking", Thinking: "summary", Signature: "sig"}, `{"type":"thinking","thinking":"summary","signature":"sig"}`},
		{"other block unchanged", AnthropicContentBlock{Type: "image", Source: &AnthropicImageSource{Type: "base64", MediaType: "image/png", Data: "test"}}, `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"test"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatal(err)
			}
			var got, want map[string]any
			if err = json.Unmarshal(b, &got); err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal([]byte(tc.want), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("wire %s; want %s", b, tc.want)
			}
		})
	}
}

func TestResponsesEmptyReasoningWireBeforeText(t *testing.T) {
	state := NewResponsesStreamState()
	var events []AnthropicStreamEvent
	inputs := []ResponseStreamEvent{
		{Type: "response.created", Response: &ResponsesResult{ID: "synthetic", Model: "gpt-6-astra"}},
		{Type: "response.output_item.done", OutputIndex: 0, Item: &ResponseOutputItem{Type: "reasoning", ID: "rs_test", EncryptedContent: "opaque-test"}},
		{Type: "response.output_text.delta", OutputIndex: 1, ContentIndex: 0, Delta: "Synthetic answer."},
		{Type: "response.output_text.done", OutputIndex: 1, ContentIndex: 0, Text: "Synthetic answer."},
		{Type: "response.completed", Response: &ResponsesResult{ID: "synthetic", Model: "gpt-6-astra", Status: "completed"}},
	}
	for _, in := range inputs {
		events = append(events, TranslateResponsesStreamEvent(in, state)...)
	}
	b, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("WIRE_FIXTURE=%s", b)
	var wire []map[string]any
	if err = json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	seenEmptyThinking := false
	open := map[int]bool{}
	next := 0
	for _, event := range wire {
		kind := event["type"]
		if kind == "content_block_start" {
			index := int(event["index"].(float64))
			if index != next {
				t.Fatalf("noncontiguous index %d want %d", index, next)
			}
			next++
			open[index] = true
			block := event["content_block"].(map[string]any)
			if block["type"] == "thinking" {
				for _, field := range []string{"thinking", "signature"} {
					if _, ok := block[field].(string); !ok {
						t.Errorf("missing thinking block %s: %v", field, block)
					}
				}
			}
			if block["type"] == "text" {
				if _, ok := block["text"].(string); !ok {
					t.Errorf("missing text block text: %v", block)
				}
			}
		}
		if kind == "content_block_delta" {
			index := int(event["index"].(float64))
			if !open[index] {
				t.Fatal("delta outside block")
			}
			d := event["delta"].(map[string]any)
			if d["type"] == "thinking_delta" {
				value, ok := d["thinking"].(string)
				if !ok {
					t.Errorf("Office thinking callback receives undefined rather than string: %v", d)
				}
				seenEmptyThinking = ok && value == ""
			}
		}
		if kind == "content_block_stop" {
			index := int(event["index"].(float64))
			if !open[index] {
				t.Fatal("double block close")
			}
			delete(open, index)
		}
	}
	if !seenEmptyThinking {
		t.Error("missing required empty thinking string")
	}
	if len(open) != 0 || !state.MessageCompleted {
		t.Fatal("incomplete stream")
	}
}

// Marshal-only compatibility fixes must not introduce inbound validation or
// alter how missing/empty tool input is decoded from client messages.
func TestBlockWireUnmarshalPreserved(t *testing.T) {
	for _, raw := range []string{`{"type":"thinking"}`, `{"type":"thinking","thinking":"","signature":""}`} {
		var block AnthropicContentBlock
		if err := json.Unmarshal([]byte(raw), &block); err != nil {
			t.Fatal(err)
		}
		if block.Type != "thinking" || block.Thinking != "" || block.Signature != "" {
			t.Fatalf("unexpected decoded block: %+v", block)
		}
	}
	for _, raw := range []string{`{"type":"tool_use"}`, `{"type":"tool_use","input":{}}`} {
		var block AnthropicContentBlock
		if err := json.Unmarshal([]byte(raw), &block); err != nil {
			t.Fatal(err)
		}
		if (block.Input == nil) != (raw == `{"type":"tool_use"}`) {
			t.Fatal("nil/empty input decoding changed")
		}
	}
}
