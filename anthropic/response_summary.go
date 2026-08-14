package anthropic

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
)

// shouldForceNativeMessages keeps Claude Opus requests on Anthropic's native
// Messages endpoint. Claude Code speaks the Messages protocol natively; routing
// Opus through Chat Completions loses protocol fidelity and can turn agent turns
// into thinking-only end turns when the model capability catalog fluctuates.
func shouldForceNativeMessages(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(model, "claude-opus")
}

type anthropicResponseSummary struct {
	StopReason     string
	ThinkingBlocks int
	TextBlocks     int
	ToolUseBlocks  int
	OtherBlocks    int
}

func (s *anthropicResponseSummary) observeBlock(block *AnthropicContentBlock) {
	if block == nil {
		return
	}
	switch block.Type {
	case "thinking":
		s.ThinkingBlocks++
	case "text":
		s.TextBlocks++
	case "tool_use":
		s.ToolUseBlocks++
	default:
		s.OtherBlocks++
	}
}

func (s *anthropicResponseSummary) observeResponse(resp *AnthropicMessagesResponse) {
	if resp == nil {
		return
	}
	if resp.StopReason != "" {
		s.StopReason = resp.StopReason
	}
	for i := range resp.Content {
		s.observeBlock(&resp.Content[i])
	}
}

func (s *anthropicResponseSummary) observeEvents(events []AnthropicStreamEvent) {
	for i := range events {
		event := &events[i]
		if event.Type == "content_block_start" {
			s.observeBlock(event.ContentBlock)
		}
		if event.Type == "message_delta" {
			s.observeDelta(event.Delta)
		}
	}
}

func (s *anthropicResponseSummary) observeDelta(delta interface{}) {
	switch value := delta.(type) {
	case *AnthropicMessageDelta:
		if value != nil && value.StopReason != "" {
			s.StopReason = value.StopReason
		}
	case AnthropicMessageDelta:
		if value.StopReason != "" {
			s.StopReason = value.StopReason
		}
	case map[string]interface{}:
		if stopReason, ok := value["stop_reason"].(string); ok && stopReason != "" {
			s.StopReason = stopReason
		}
	case json.RawMessage:
		var messageDelta AnthropicMessageDelta
		if json.Unmarshal(value, &messageDelta) == nil && messageDelta.StopReason != "" {
			s.StopReason = messageDelta.StopReason
		}
	}
}

// observeNativeStreamLine inspects an Anthropic SSE data line without changing
// the bytes forwarded to the client.
func (s *anthropicResponseSummary) observeNativeStreamLine(line []byte) {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, []byte("data:")) {
		return
	}
	payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return
	}

	var event struct {
		Type         string                 `json:"type"`
		ContentBlock *AnthropicContentBlock `json:"content_block"`
		Delta        json.RawMessage        `json:"delta"`
	}
	if json.Unmarshal(payload, &event) != nil {
		return
	}
	if event.Type == "content_block_start" {
		s.observeBlock(event.ContentBlock)
	}
	if event.Type == "message_delta" {
		s.observeDelta(event.Delta)
	}
}

func logAnthropicResponseSummary(route, model string, summary anthropicResponseSummary) {
	actionableBlocks := summary.TextBlocks + summary.ToolUseBlocks
	slog.Info("anthropic response summary",
		"route", route,
		"model", model,
		"stop_reason", summary.StopReason,
		"thinking_blocks", summary.ThinkingBlocks,
		"text_blocks", summary.TextBlocks,
		"tool_use_blocks", summary.ToolUseBlocks,
		"other_blocks", summary.OtherBlocks,
		"actionable_blocks", actionableBlocks,
		"thinking_only", summary.ThinkingBlocks > 0 && actionableBlocks == 0,
	)
}
