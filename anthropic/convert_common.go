package anthropic

import (
	"fmt"
	"strings"
)

// extractSystemText extracts concatenated text from an AnthropicSystem value.
// Returns nil when no text content is found.
func extractSystemText(system *AnthropicSystem) *string {
	if system == nil {
		return nil
	}
	if system.Text != nil {
		return system.Text
	}
	if len(system.Blocks) > 0 {
		var parts []string
		for _, block := range system.Blocks {
			if block.Type == "text" {
				parts = append(parts, block.Text)
			}
		}
		if len(parts) > 0 {
			combined := strings.Join(parts, "\n\n")
			return &combined
		}
	}
	return nil
}

// canonicalizeSystemMessages accepts the OpenAI-style system roles that some
// Anthropic-compatible clients put in messages, and folds them into Anthropic's
// canonical top-level system field. The Messages API itself only permits user
// and assistant roles, but rejecting this common compatibility shape prevents
// Claude Code from using non-Anthropic upstream models through the proxy.
//
// System instructions must never be silently dropped: Responses consumes the
// canonical value via instructions, while Chat Completions emits a system
// message. Keep the top-level system first, then append in-message systems in
// their original order.
func canonicalizeSystemMessages(system *AnthropicSystem, messages []AnthropicMessage) (*AnthropicSystem, []AnthropicMessage, error) {
	var instructionParts []string
	if text := extractSystemText(system); text != nil && *text != "" {
		instructionParts = append(instructionParts, *text)
	}

	kept := make([]AnthropicMessage, 0, len(messages))
	for _, msg := range messages {
		if msg.Role != "system" {
			kept = append(kept, msg)
			continue
		}

		text, err := extractSystemMessageText(msg.Content)
		if err != nil {
			return nil, nil, err
		}
		if text != "" {
			instructionParts = append(instructionParts, text)
		}
	}

	if len(instructionParts) == 0 {
		return nil, kept, nil
	}
	combined := strings.Join(instructionParts, "\n\n")
	return &AnthropicSystem{Text: &combined}, kept, nil
}

func extractSystemMessageText(content AnthropicContent) (string, error) {
	if content.Text != nil {
		return *content.Text, nil
	}

	parts := make([]string, 0, len(content.Blocks))
	for _, block := range content.Blocks {
		if block.Type != "text" {
			return "", fmt.Errorf("system message content block type %q is unsupported; expected text", block.Type)
		}
		parts = append(parts, block.Text)
	}
	return strings.Join(parts, "\n\n"), nil
}

// ToolNameDescSchema holds the fields common to every tool definition format.
type ToolNameDescSchema struct {
	Name        string
	Description string
	InputSchema interface{}
}

// extractToolDefs converts Anthropic tool definitions into a format-neutral slice.
func extractToolDefs(tools []AnthropicTool) []ToolNameDescSchema {
	defs := make([]ToolNameDescSchema, len(tools))
	for i, t := range tools {
		defs[i] = ToolNameDescSchema{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		}
	}
	return defs
}

// ToolChoiceResult holds the normalised tool-choice mapping that both the
// Chat-Completions and Responses code paths need.
type ToolChoiceResult struct {
	// Mode is one of "auto", "required", "none", or "function".
	Mode string
	// FunctionName is set only when Mode == "function".
	FunctionName string
	// DisableParallelCalls mirrors the Anthropic field (nil = unset).
	DisableParallelCalls *bool
}

// mapToolChoice converts an AnthropicToolChoice to a normalised result.
func mapToolChoice(choice *AnthropicToolChoice) ToolChoiceResult {
	if choice == nil {
		return ToolChoiceResult{Mode: "auto"}
	}
	res := ToolChoiceResult{DisableParallelCalls: choice.DisableParallelCalls}
	switch choice.Type {
	case "auto":
		res.Mode = "auto"
	case "any":
		res.Mode = "required"
	case "tool":
		if choice.Name != "" {
			res.Mode = "function"
			res.FunctionName = choice.Name
		} else {
			res.Mode = "auto"
		}
	case "none":
		res.Mode = "none"
	default:
		res.Mode = "auto"
	}
	return res
}

// imageDataURL builds a data-URI from a media type and base64 payload.
func imageDataURL(mediaType, data string) string {
	return fmt.Sprintf("data:%s;base64,%s", mediaType, data)
}

// adjustCachedUsage subtracts cached tokens from input tokens (clamped to 0)
// and returns the adjusted input tokens together with the cache-read count.
func adjustCachedUsage(inputTokens, cachedTokens int) (adjustedInput, cacheRead int) {
	adjustedInput = inputTokens - cachedTokens
	if adjustedInput < 0 {
		adjustedInput = 0
	}
	return adjustedInput, cachedTokens
}
