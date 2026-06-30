package anthropic

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRewriteNativeResponseModel(t *testing.T) {
	in := []byte(`{"id":"msg_1","type":"message","model":"claude-opus-4-6","content":[]}`)
	out, upstream, changed := rewriteNativeResponseModel(in, "claude-opus-4.6")
	if !changed {
		t.Fatalf("expected changed=true")
	}
	if upstream != "claude-opus-4-6" {
		t.Fatalf("expected upstream=claude-opus-4-6, got %q", upstream)
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("invalid json out: %v", err)
	}
	if obj["model"] != "claude-opus-4.6" {
		t.Fatalf("expected model rewritten to claude-opus-4.6, got %v", obj["model"])
	}
	// Other fields preserved
	if obj["id"] != "msg_1" {
		t.Fatalf("expected id preserved, got %v", obj["id"])
	}
}

func TestRewriteNativeResponseModel_NoChangeWhenEqual(t *testing.T) {
	in := []byte(`{"model":"claude-opus-4.6"}`)
	_, upstream, changed := rewriteNativeResponseModel(in, "claude-opus-4.6")
	if changed {
		t.Fatalf("expected changed=false when already equal")
	}
	if upstream != "claude-opus-4.6" {
		t.Fatalf("expected upstream reported, got %q", upstream)
	}
}

func TestRewriteNativeResponseModel_EmptyRequested(t *testing.T) {
	in := []byte(`{"model":"claude-opus-4-6"}`)
	out, _, changed := rewriteNativeResponseModel(in, "")
	if changed {
		t.Fatalf("expected no change when requested empty")
	}
	if string(out) != string(in) {
		t.Fatalf("expected bytes unchanged")
	}
}

func TestRewriteNativeStreamLineModel_MessageStart(t *testing.T) {
	line := []byte(`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-opus-4-6","content":[]}}` + "\n")
	out, upstream, changed := rewriteNativeStreamLineModel(line, "claude-opus-4.6")
	if !changed {
		t.Fatalf("expected changed=true")
	}
	if upstream != "claude-opus-4-6" {
		t.Fatalf("expected upstream=claude-opus-4-6, got %q", upstream)
	}
	if !strings.HasSuffix(string(out), "\n") {
		t.Fatalf("expected trailing newline preserved")
	}
	if !strings.Contains(string(out), `"claude-opus-4.6"`) {
		t.Fatalf("expected rewritten model in line, got %s", out)
	}
	if strings.Contains(string(out), `"claude-opus-4-6"`) {
		t.Fatalf("expected upstream model removed, got %s", out)
	}
}

func TestRewriteNativeStreamLineModel_NonMessageStartUnchanged(t *testing.T) {
	line := []byte(`data: {"type":"content_block_delta","delta":{"text":"hi"}}` + "\n")
	out, _, changed := rewriteNativeStreamLineModel(line, "claude-opus-4.6")
	if changed {
		t.Fatalf("expected no change for non-message_start")
	}
	if string(out) != string(line) {
		t.Fatalf("expected line unchanged")
	}
}

func TestRewriteNativeStreamLineModel_DoneUnchanged(t *testing.T) {
	line := []byte("data: [DONE]\n")
	_, _, changed := rewriteNativeStreamLineModel(line, "claude-opus-4.6")
	if changed {
		t.Fatalf("expected no change for [DONE]")
	}
}

func TestOverrideStreamEventModel(t *testing.T) {
	model := "claude-opus-4-6"
	events := []AnthropicStreamEvent{
		{Type: "message_start", Message: &AnthropicMessagesResponse{Model: model}},
		{Type: "content_block_delta"},
	}
	overrideStreamEventModel(events, "claude-opus-4.6")
	if events[0].Message.Model != "claude-opus-4.6" {
		t.Fatalf("expected message_start model overridden, got %q", events[0].Message.Model)
	}
}

func TestOverrideStreamEventModel_EmptyNoop(t *testing.T) {
	events := []AnthropicStreamEvent{
		{Type: "message_start", Message: &AnthropicMessagesResponse{Model: "claude-opus-4-6"}},
	}
	overrideStreamEventModel(events, "")
	if events[0].Message.Model != "claude-opus-4-6" {
		t.Fatalf("expected no change when newModel empty")
	}
}
