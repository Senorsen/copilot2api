package anthropic

import "testing"

func TestShouldForceNativeMessages(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{model: "claude-opus-5", want: true},
		{model: "claude-opus-4.6", want: true},
		{model: " Claude-Opus-5 ", want: true},
		{model: "claude-sonnet-4.6", want: false},
		{model: "claude-haiku-4.5", want: false},
		{model: "gpt-5.6", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			if got := shouldForceNativeMessages(tt.model); got != tt.want {
				t.Fatalf("shouldForceNativeMessages(%q) = %v, want %v", tt.model, got, tt.want)
			}
		})
	}
}

func TestAnthropicResponseSummaryNativeStreamThinkingOnly(t *testing.T) {
	lines := [][]byte{
		[]byte("event: content_block_start\n"),
		[]byte("data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n"),
		[]byte("data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null}}\n"),
	}

	summary := anthropicResponseSummary{}
	for _, line := range lines {
		summary.observeNativeStreamLine(line)
	}

	if summary.ThinkingBlocks != 1 || summary.TextBlocks != 0 || summary.ToolUseBlocks != 0 {
		t.Fatalf("unexpected block counts: %+v", summary)
	}
	if summary.StopReason != "end_turn" {
		t.Fatalf("stop reason = %q, want end_turn", summary.StopReason)
	}
}

func TestAnthropicResponseSummaryConvertedEvents(t *testing.T) {
	index := 0
	events := []AnthropicStreamEvent{
		{Type: "content_block_start", Index: &index, ContentBlock: &AnthropicContentBlock{Type: "thinking"}},
		{Type: "content_block_start", Index: &index, ContentBlock: &AnthropicContentBlock{Type: "tool_use", Name: "Bash"}},
		{Type: "message_delta", Delta: &AnthropicMessageDelta{StopReason: "tool_use"}},
	}

	summary := anthropicResponseSummary{}
	summary.observeEvents(events)

	if summary.ThinkingBlocks != 1 || summary.ToolUseBlocks != 1 || summary.TextBlocks != 0 {
		t.Fatalf("unexpected block counts: %+v", summary)
	}
	if summary.StopReason != "tool_use" {
		t.Fatalf("stop reason = %q, want tool_use", summary.StopReason)
	}
}
