package anthropic

import (
	"encoding/json"
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

// signaturePathPattern captures the message index named in errors such as
// "messages.1.content.37: Invalid `signature` in `thinking` block".
var signaturePathPattern = regexp.MustCompile(`messages\.(\d+)\.`)

// offendingMessageIndex returns the message index named in the upstream error,
// or -1 when the error does not identify one.
func offendingMessageIndex(body []byte) int {
	m := signaturePathPattern.FindStringSubmatch(upstreamErrorMessage(body))
	if m == nil {
		return -1
	}
	idx, err := strconv.Atoi(m[1])
	if err != nil {
		return -1
	}
	return idx
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

// stripThinkingSignatures removes the signature field from thinking blocks in
// a native /v1/messages request body, returning the rewritten body and how
// many signatures were dropped.
//
// When msgIndex is >= 0 only that message is repaired, which is what the
// upstream error identifies. Anthropic reports just the first offending block,
// so a caller that still gets rejected can widen the scope by passing -1 to
// clear every message.
//
// The body is walked as generic JSON rather than through the typed request
// structs: this path is a verbatim passthrough, and round-tripping it through
// our own types would silently drop any field we do not model.
func stripThinkingSignatures(body []byte, msgIndex int) ([]byte, int) {
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return body, 0
	}

	messages, ok := root["messages"].([]any)
	if !ok {
		return body, 0
	}

	removed := 0
	for i, m := range messages {
		if msgIndex >= 0 && i != msgIndex {
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
		for _, b := range blocks {
			block, ok := b.(map[string]any)
			if !ok {
				continue
			}
			// redacted_thinking blocks are already opaque and carry no
			// signature field; only "thinking" needs repairing.
			if block["type"] != "thinking" {
				continue
			}
			if _, present := block["signature"]; present {
				delete(block, "signature")
				removed++
			}
		}
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

// retryWithoutThinkingSignatures returns a repaired body when the upstream
// error is an invalid thinking signature and at least one signature could be
// removed. The second result reports whether a retry is worth attempting.
//
// Repair is scoped to the message the upstream error names, so signatures on
// unrelated turns are left intact. Only when the error names no message (or
// that message turns out to hold no signature) does it fall back to clearing
// every one, which is still preferable to a conversation that can never
// recover.
func retryWithoutThinkingSignatures(body, errBody []byte, endpoint string) ([]byte, bool) {
	if !invalidThinkingSignature(errBody) {
		return nil, false
	}

	idx := offendingMessageIndex(errBody)
	repaired, removed := stripThinkingSignatures(body, idx)
	scope := "message " + strconv.Itoa(idx)

	if removed == 0 && idx >= 0 {
		// The named message held nothing to strip, so fall back to a full sweep
		// rather than replaying an identical request.
		repaired, removed = stripThinkingSignatures(body, -1)
		scope = "all messages (fallback)"
	}

	if removed == 0 {
		slog.Warn("upstream rejected a thinking signature but none were found to strip",
			"endpoint", endpoint,
			"upstream_error", upstreamErrorMessage(errBody))
		return nil, false
	}

	slog.Warn("stripped invalid thinking signatures and retrying",
		"endpoint", endpoint,
		"scope", scope,
		"signatures_removed", removed,
		"upstream_error", upstreamErrorMessage(errBody))
	return repaired, true
}
