package anthropic

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
)

// Anthropic rejects a request outright when any thinking block carries a
// signature it cannot verify:
//
//	messages.1.content.37: Invalid `signature` in `thinking` block
//
// The signature is an opaque upstream artifact, so a client that replays a
// stale or re-encoded one has no way to repair it and the whole conversation
// becomes unusable — every follow-up turn resends the same history and fails
// again. Dropping the offending signature costs at most some reasoning
// context on that turn, which is a far better outcome than a dead session.
//
// The retry is deliberately narrow: it only fires on a 400 whose message
// names a thinking-block signature, and it only edits thinking blocks.

// invalidThinkingSignature reports whether an upstream error body is the
// specific "invalid signature in thinking block" rejection.
func invalidThinkingSignature(body []byte) bool {
	msg := upstreamErrorMessage(body)
	if msg == "" {
		return false
	}
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "signature") && strings.Contains(lower, "thinking")
}

// signaturePathPattern captures the message and content-block indices named
// in errors such as "messages.1.content.37: Invalid `signature` in `thinking`
// block".
var signaturePathPattern = regexp.MustCompile(`messages\.(\d+)\.content\.(\d+)`)

// offendingLocation returns the message and content-block indices named in the
// upstream error. Either value is -1 when the error does not identify it.
func offendingLocation(body []byte) (msgIdx, blockIdx int) {
	m := signaturePathPattern.FindStringSubmatch(upstreamErrorMessage(body))
	if m == nil {
		return -1, -1
	}
	msgIdx, err := strconv.Atoi(m[1])
	if err != nil {
		return -1, -1
	}
	blockIdx, err = strconv.Atoi(m[2])
	if err != nil {
		return msgIdx, -1
	}
	return msgIdx, blockIdx
}

// upstreamErrorMessage pulls error.message out of an Anthropic error body.
func upstreamErrorMessage(body []byte) string {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	return parsed.Error.Message
}

// stripThinkingSignatures removes thinking blocks whose signature upstream
// cannot verify, returning the rewritten body and how many blocks were
// dropped.
//
// The whole block is removed rather than just its signature field: signature
// is required on a thinking block, so clearing the field alone trades one
// rejection for another ("thinking.signature: Field required").
//
// Blocks in the final assistant message are never touched. With thinking
// enabled that message must begin with a thinking block, so removing it there
// swaps the signature error for "Expected `thinking` or `redacted_thinking`".
// Earlier turns carry no such requirement — upstream ignores their thinking
// blocks anyway — which is exactly where stale signatures accumulate.
//
// msgIdx and blockIdx narrow the repair to the exact block upstream named;
// either may be -1 to widen the scope to a whole message or the whole request.
//
// The body is walked as generic JSON rather than through the typed request
// structs: this path is a verbatim passthrough, and round-tripping it through
// our own types would silently drop any field we do not model.
func stripThinkingSignatures(body []byte, msgIdx, blockIdx int) ([]byte, int) {
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return body, 0
	}

	messages, ok := root["messages"].([]any)
	if !ok {
		return body, 0
	}

	lastAssistant := protectedAssistantIndex(messages)

	removed := 0
	for i, m := range messages {
		if msgIdx >= 0 && i != msgIdx {
			continue
		}
		if i == lastAssistant {
			continue
		}
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		blocks, ok := msg["content"].([]any)
		if !ok {
			continue
		}

		kept := make([]any, 0, len(blocks))
		for j, b := range blocks {
			if blockIdx >= 0 && j != blockIdx {
				kept = append(kept, b)
				continue
			}
			block, ok := b.(map[string]any)
			if ok && block["type"] == "thinking" {
				if _, present := block["signature"]; present {
					removed++
					continue
				}
			}
			kept = append(kept, b)
		}

		// An assistant turn stripped down to nothing would be an invalid
		// message, so leave such a turn alone rather than producing a
		// differently broken request.
		if len(kept) == 0 && len(blocks) > 0 {
			removed = 0
			break
		}
		msg["content"] = kept
	}

	if removed == 0 {
		return body, 0
	}

	rewritten, err := json.Marshal(root)
	if err != nil {
		return body, 0
	}
	return rewritten, removed
}

// describeBlockTarget explains why the block upstream named was left in place.
// Without this the "could not be dropped" warning gives nothing to act on: the
// cause could be message shape, block type, a protected position, or indices
// that do not line up with the body we hold.
func describeBlockTarget(body []byte, msgIdx, blockIdx int) string {
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return "body is not JSON"
	}
	messages, ok := root["messages"].([]any)
	if !ok {
		return "no messages array"
	}
	if msgIdx >= len(messages) {
		return fmt.Sprintf("message index out of range (have %d)", len(messages))
	}
	if protectedAssistantIndex(messages) == msgIdx {
		return "message is the trailing assistant turn, which must keep its thinking block"
	}
	msg, ok := messages[msgIdx].(map[string]any)
	if !ok {
		return "message is not an object"
	}
	blocks, ok := msg["content"].([]any)
	if !ok {
		return fmt.Sprintf("content is %T, not a block list", msg["content"])
	}
	if blockIdx >= len(blocks) {
		return fmt.Sprintf("block index out of range (message has %d blocks)", len(blocks))
	}
	if len(blocks) == 1 {
		return "removing the only block would leave an empty message"
	}
	block, ok := blocks[blockIdx].(map[string]any)
	if !ok {
		return "block is not an object"
	}
	if block["type"] != "thinking" {
		return fmt.Sprintf("block type is %v, not thinking", block["type"])
	}
	if _, present := block["signature"]; !present {
		return "thinking block carries no signature field"
	}
	return "unknown"
}

// protectedAssistantIndex returns the index of a trailing assistant message
// that must keep its thinking blocks, or -1 when none is protected.
//
// With thinking enabled the request's final assistant message has to begin
// with a thinking block, so stripping one there swaps the signature error for
// "Expected `thinking` or `redacted_thinking`". The requirement applies only
// while that message is still the last one in the conversation; once a user
// turn follows, it is ordinary history whose thinking upstream ignores.
func protectedAssistantIndex(messages []any) int {
	for i := len(messages) - 1; i >= 0; i-- {
		msg, ok := messages[i].(map[string]any)
		if !ok {
			return -1
		}
		switch msg["role"] {
		case "assistant":
			return i
		case "user":
			// A user turn closes the previous assistant message, lifting the
			// leading-thinking-block requirement from it.
			return -1
		}
	}
	return -1
}

// retryWithoutThinkingSignatures returns a repaired body when the upstream
// error is an invalid thinking signature and at least one block could be
// dropped. The second result reports whether a retry is worth attempting.
//
// Repair starts at the exact block the error names and widens only as far as
// needed, so reasoning context on unrelated turns survives.
// retryWithoutThinkingSignatures returns a repaired body when the upstream
// error names a thinking block that can be dropped. The second result reports
// whether a retry is worth attempting.
//
// Only the exact block named in the error is removed. Anthropic identifies it
// as messages.<n>.content.<m>, so there is no need to guess: broader sweeps
// would discard reasoning context from turns upstream never objected to.
func retryWithoutThinkingSignatures(body, errBody []byte, endpoint string) ([]byte, bool) {
	if !invalidThinkingSignature(errBody) {
		return nil, false
	}

	msgIdx, blockIdx := offendingLocation(errBody)
	if msgIdx < 0 || blockIdx < 0 {
		slog.Warn("upstream rejected a thinking signature without naming a block",
			"endpoint", endpoint,
			"upstream_error", upstreamErrorMessage(errBody))
		return nil, false
	}

	repaired, removed := stripThinkingSignatures(body, msgIdx, blockIdx)
	if removed == 0 {
		slog.Warn("upstream rejected a thinking signature but the named block could not be dropped",
			"endpoint", endpoint,
			"message_index", msgIdx,
			"block_index", blockIdx,
			"reason", describeBlockTarget(body, msgIdx, blockIdx),
			"upstream_error", upstreamErrorMessage(errBody))
		return nil, false
	}

	slog.Warn("dropped the unverifiable thinking block and retrying",
		"endpoint", endpoint,
		"message_index", msgIdx,
		"block_index", blockIdx,
		"upstream_error", upstreamErrorMessage(errBody))
	return repaired, true
}
