package anthropic

import (
	"log/slog"
)

// Claude Code replays whole conversations, including thinking blocks from
// earlier turns. Those blocks carry a signature that upstream verifies, and
// once one of them stops verifying the conversation is finished: the client
// resends the same history every turn, so "Invalid `signature` in `thinking`
// block" repeats forever.
//
// The offending block cannot be identified from the error. Upstream reported
// messages.1.content.30 for a message that held two blocks, so its indices
// describe its own reconstruction of the history rather than the body we sent.
// Removing only the blocks with empty reasoning text was not enough either.
// Every signed thinking block in the history is therefore dropped.
//
// Nothing readable is lost. Upstream ignores thinking blocks from previous
// turns and excludes them from context accounting, so they influence the reply
// only by failing verification.
//
// Two cases are deliberately preserved:
//   - any assistant message containing tool_use, because upstream validates
//     that a tool-calling turn still carries its thinking; stripping there
//     breaks the agentic loop and the model stops early instead of continuing
//   - the trailing assistant message, which must still begin with a thinking
//     block while thinking is enabled
//   - a message's sole block, since emptying the content list would make the
//     message invalid
func stripEmptyThinkingBlocks(obj map[string]any) {
	messages, ok := obj["messages"].([]any)
	if !ok {
		return
	}

	protected := protectedAssistantIndex(messages)
	removed := 0

	for i, m := range messages {
		if i == protected {
			continue
		}
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		blocks, ok := msg["content"].([]any)
		if !ok || len(blocks) <= 1 {
			continue
		}
		if containsToolUse(blocks) {
			continue
		}

		kept := make([]any, 0, len(blocks))
		for _, b := range blocks {
			if isSignedThinkingBlock(b) {
				removed++
				continue
			}
			kept = append(kept, b)
		}
		if len(kept) == 0 {
			continue
		}
		msg["content"] = kept
	}

	if removed > 0 {
		// Info, not Debug: this is the guard against a class of failure that
		// previously bricked whole sessions, so it needs to be visible in
		// production logs without raising the level.
		slog.Info("dropped historical thinking blocks before forwarding",
			"blocks_removed", removed)
	}
}

// containsToolUse reports whether a content list holds a tool_use block.
func containsToolUse(blocks []any) bool {
	for _, b := range blocks {
		block, ok := b.(map[string]any)
		if ok && block["type"] == "tool_use" {
			return true
		}
	}
	return false
}

// isSignedThinkingBlock reports whether a content block is a thinking block
// carrying a signature, which is what upstream verifies and can reject.
//
// redacted_thinking is a distinct type and is left alone. A thinking block
// without a signature cannot trigger the rejection, so it is kept as well.
func isSignedThinkingBlock(b any) bool {
	block, ok := b.(map[string]any)
	if !ok || block["type"] != "thinking" {
		return false
	}
	sig, _ := block["signature"].(string)
	return sig != ""
}
