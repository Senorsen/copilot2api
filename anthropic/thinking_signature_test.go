package anthropic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// The upstream body reproduced here is the exact rejection observed on the JP
// relay, where a Claude Code session failed 20+ consecutive times against the
// same replayed history.
const realRejection = `{"type":"error","error":{"type":"invalid_request_error","message":"messages.1.content.37: Invalid ` + "`signature`" + ` in ` + "`thinking`" + ` block"},"request_id":"req_011Cdo5S6fqNJxKdL5JfgYvW"}`

// conversation builds a request whose messages[1] holds `blocks` content
// blocks, with a thinking block carrying `sig` at index `sigAt`. A trailing
// user turn keeps messages[1] out of the final-assistant position, which is
// protected. This mirrors the real failure: a long assistant turn deep in the
// history, not a two-block toy.
func conversation(blocks, sigAt int, sig string) []byte {
	parts := make([]string, blocks)
	for i := range parts {
		if i == sigAt {
			parts[i] = fmt.Sprintf(`{"type":"thinking","thinking":"reasoning","signature":%q}`, sig)
			continue
		}
		parts[i] = fmt.Sprintf(`{"type":"text","text":"block-%d"}`, i)
	}
	return []byte(fmt.Sprintf(`{
		"model": "claude-opus-5",
		"messages": [
			{"role":"user","content":[{"type":"text","text":"hi"}]},
			{"role":"assistant","content":[%s]},
			{"role":"user","content":[{"type":"text","text":"next"}]}
		]
	}`, strings.Join(parts, ",")))
}

func TestInvalidThinkingSignatureMatchesRealRejection(t *testing.T) {
	if !invalidThinkingSignature([]byte(realRejection)) {
		t.Fatal("failed to recognise the observed upstream rejection")
	}
}

func TestInvalidThinkingSignatureIgnoresUnrelatedErrors(t *testing.T) {
	cases := map[string]string{
		"rate limit":      `{"error":{"message":"rate limit exceeded"}}`,
		"other signature": `{"error":{"message":"Invalid signature in tool_use block"}}`,
		"other thinking":  `{"error":{"message":"thinking blocks must be first"}}`,
		"empty":           ``,
		"not json":        `<html>413</html>`,
	}
	for name, body := range cases {
		if invalidThinkingSignature([]byte(body)) {
			t.Errorf("%s: should not be treated as an invalid thinking signature", name)
		}
	}
}

func TestOffendingLocation(t *testing.T) {
	// content.37 is an index into the message's content array, so both numbers
	// must be recovered to target the right block.
	msg, block := offendingLocation([]byte(realRejection))
	if msg != 1 || block != 37 {
		t.Errorf("got (%d, %d), want (1, 37) from messages.1.content.37", msg, block)
	}

	msg, block = offendingLocation([]byte(`{"error":{"message":"messages.12.content.3: Invalid ` + "`signature`" + `"}}`))
	if msg != 12 || block != 3 {
		t.Errorf("got (%d, %d), want (12, 3)", msg, block)
	}

	if msg, block = offendingLocation([]byte(`{"error":{"message":"something went wrong"}}`)); msg != -1 || block != -1 {
		t.Errorf("got (%d, %d), want (-1, -1) when nothing is named", msg, block)
	}
}

func TestRemovesOnlyTheNamedBlock(t *testing.T) {
	body := conversation(40, 37, "stale-sig")

	repaired, ok := retryWithoutThinkingSignatures(body, []byte(realRejection), "/v1/messages")
	if !ok {
		t.Fatal("expected a retry for the observed rejection")
	}
	if bytes.Contains(repaired, []byte("stale-sig")) {
		t.Error("the named thinking block should have been removed")
	}

	var root map[string]any
	if err := json.Unmarshal(repaired, &root); err != nil {
		t.Fatalf("rewritten body is not valid JSON: %v", err)
	}
	if root["model"] != "claude-opus-5" {
		t.Error("unrelated top-level fields must survive the rewrite")
	}

	msgs := root["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("message count changed: %d", len(msgs))
	}
	blocks := msgs[1].(map[string]any)["content"].([]any)
	if len(blocks) != 39 {
		t.Fatalf("content length = %d, want 39 (exactly one block dropped)", len(blocks))
	}
	// Every surviving block must be one of the originals, in order.
	for i, b := range blocks {
		want := i
		if i >= 37 {
			want = i + 1 // the removed block shifted the tail down
		}
		if got := b.(map[string]any)["text"]; got != fmt.Sprintf("block-%d", want) {
			t.Errorf("block %d = %v, want block-%d", i, got, want)
		}
	}
}

func TestKeepsThinkingBlocksOnOtherMessages(t *testing.T) {
	// Two assistant turns both carry signatures; only the one upstream named
	// may be touched, so reasoning context elsewhere is not thrown away.
	body := []byte(`{"messages":[
		{"role":"user","content":[{"type":"text","text":"hi"}]},
		{"role":"assistant","content":[
			{"type":"text","text":"a"},
			{"type":"thinking","signature":"drop-me"}
		]},
		{"role":"assistant","content":[{"type":"thinking","signature":"keep-me"}]},
		{"role":"user","content":[{"type":"text","text":"next"}]}
	]}`)
	errBody := []byte(`{"error":{"message":"messages.1.content.1: Invalid ` + "`signature`" + ` in ` + "`thinking`" + ` block"}}`)

	repaired, ok := retryWithoutThinkingSignatures(body, errBody, "/v1/messages")
	if !ok {
		t.Fatal("expected a retry")
	}
	if bytes.Contains(repaired, []byte("drop-me")) {
		t.Error("named block should have been removed")
	}
	if !bytes.Contains(repaired, []byte("keep-me")) {
		t.Error("thinking blocks on other messages must be preserved")
	}
}

func TestProtectsFinalAssistantMessage(t *testing.T) {
	// With thinking enabled the final assistant message must start with a
	// thinking block, so removing one there trades the signature error for
	// "Expected `thinking` or `redacted_thinking`".
	body := []byte(`{"messages":[
		{"role":"user","content":[{"type":"text","text":"hi"}]},
		{"role":"assistant","content":[
			{"type":"thinking","signature":"last-turn"},
			{"type":"tool_use","id":"t1","name":"bash","input":{}}
		]}
	]}`)
	errBody := []byte(`{"error":{"message":"messages.1.content.0: Invalid ` + "`signature`" + ` in ` + "`thinking`" + ` block"}}`)

	if _, ok := retryWithoutThinkingSignatures(body, errBody, "/v1/messages"); ok {
		t.Error("must not strip thinking from the final assistant message")
	}
}

func TestKeepsMessageNonEmpty(t *testing.T) {
	// Removing the only block would leave an invalid empty message.
	body := []byte(`{"messages":[
		{"role":"user","content":[{"type":"text","text":"hi"}]},
		{"role":"assistant","content":[{"type":"thinking","signature":"sole"}]},
		{"role":"user","content":[{"type":"text","text":"next"}]}
	]}`)
	errBody := []byte(`{"error":{"message":"messages.1.content.0: Invalid ` + "`signature`" + ` in ` + "`thinking`" + ` block"}}`)

	if _, ok := retryWithoutThinkingSignatures(body, errBody, "/v1/messages"); ok {
		t.Error("must not empty out a message")
	}
}

func TestNoRetryWithoutUsableTarget(t *testing.T) {
	body := conversation(40, 37, "stale-sig")

	// Unrelated failures must pass straight through.
	if _, ok := retryWithoutThinkingSignatures(body, []byte(`{"error":{"message":"overloaded"}}`), "/v1/messages"); ok {
		t.Error("unrelated errors must not be retried")
	}

	// A signature error that names no location gives us nothing to act on.
	vague := []byte(`{"error":{"message":"Invalid ` + "`signature`" + ` in ` + "`thinking`" + ` block"}}`)
	if _, ok := retryWithoutThinkingSignatures(body, vague, "/v1/messages"); ok {
		t.Error("must not retry when no block is named")
	}

	// The named index points at a text block, so there is nothing to remove
	// and retrying would replay an identical request.
	wrongTarget := []byte(`{"error":{"message":"messages.1.content.5: Invalid ` + "`signature`" + ` in ` + "`thinking`" + ` block"}}`)
	if _, ok := retryWithoutThinkingSignatures(body, wrongTarget, "/v1/messages"); ok {
		t.Error("must not retry when the named block is not a signed thinking block")
	}

	// Out-of-range indices must not panic.
	outOfRange := []byte(`{"error":{"message":"messages.99.content.99: Invalid ` + "`signature`" + ` in ` + "`thinking`" + ` block"}}`)
	if _, ok := retryWithoutThinkingSignatures(body, outOfRange, "/v1/messages"); ok {
		t.Error("must not retry on out-of-range indices")
	}
}

func TestToleratesMalformedBodies(t *testing.T) {
	// Content may legitimately be a plain string rather than a block list, and
	// a malformed body must never panic or corrupt the passthrough.
	cases := [][]byte{
		[]byte(`not json`),
		[]byte(`{"messages":"oops"}`),
		[]byte(`{"messages":[{"role":"user","content":"plain string"}]}`),
		[]byte(`{"messages":[null]}`),
		[]byte(`{}`),
	}
	for _, body := range cases {
		out, removed := stripThinkingSignatures(body, 1, 0)
		if removed != 0 {
			t.Errorf("%s: removed = %d, want 0", body, removed)
		}
		if string(out) != string(body) {
			t.Errorf("%s: body should be unchanged", body)
		}
	}
}
