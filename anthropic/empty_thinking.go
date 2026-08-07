package anthropic

import (
	"log/slog"
)

// Claude Code replays whole conversations, including thinking blocks from
// earlier turns. Under high reasoning effort the full reasoning is encrypted
// into the signature and the plaintext `thinking` field is left empty, so a
// long session accumulates a run of empty-text blocks that exist only to carry
// opaque signatures.
//
// Those are exactly the blocks upstream later rejects with
// "Invalid `signature` in `thinking` block": in the session captured on
// 2026-08-07, seven of eight thinking blocks had empty text and all seven
// shared one encrypted-context id, while the single block with real text was
// unaffected. Because the client resends the same history every turn, one
// stale block makes the conversation permanently unusable.
//
// Dropping them before the request goes out avoids the rejection entirely,
// and costs nothing readable: an empty `thinking` carries no reasoning we
// could pass on, and upstream ignores thinking from previous turns anyway.
//
// Two blocks are deliberately kept:
//   - anything in a trailing assistant message, which must still begin with a
//     thinking block while thinking is enabled
//   - the sole block of a message, since emptying the content list would make
//     the message invalid
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

		kept := make([]any, 0, len(blocks))
		for _, b := range blocks {
			if isEmptyThinkingBlock(b) {
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
		slog.Debug("dropped empty thinking blocks before forwarding",
			"blocks_removed", removed)
	}
}

// isEmptyThinkingBlock reports whether a content block is a thinking block
// whose reasoning text is empty, leaving only an opaque signature.
func isEmptyThinkingBlock(b any) bool {
	block, ok := b.(map[string]any)
	if !ok || block["type"] != "thinking" {
		return false
	}
	// A block with no signature cannot trigger the rejection this guards
	// against, so leave it alone.
	if sig, _ := block["signature"].(string); sig == "" {
		return false
	}
	text, _ := block["thinking"].(string)
	return text == ""
}
