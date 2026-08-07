package anthropic

import (
	"encoding/json"
	"testing"
)

// The upstream body reproduced here is the exact rejection observed on the JP
// relay, where a Claude Code session failed 20+ consecutive times against the
// same replayed history.
const realRejection = `{"type":"error","error":{"type":"invalid_request_error","message":"messages.1.content.37: Invalid ` + "`signature`" + ` in ` + "`thinking`" + ` block"},"request_id":"req_011Cdo5S6fqNJxKdL5JfgYvW"}`

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

func TestStripThinkingSignaturesRemovesOnlySignatures(t *testing.T) {
	body := []byte(`{
		"model": "claude-opus-5",
		"system": "be brief",
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "hi"}]},
			{"role": "assistant", "content": [
				{"type": "thinking", "thinking": "reasoning here", "signature": "stale-sig"},
				{"type": "redacted_thinking", "data": "opaque"},
				{"type": "text", "text": "answer"}
			]}
		]
	}`)

	out, removed := stripThinkingSignatures(body)
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}

	var root map[string]any
	if err := json.Unmarshal(out, &root); err != nil {
		t.Fatalf("rewritten body is not valid JSON: %v", err)
	}

	// Unrelated top-level fields must survive: this path is a verbatim
	// passthrough and dropping fields would change request semantics.
	if root["model"] != "claude-opus-5" || root["system"] != "be brief" {
		t.Errorf("top-level fields were altered: %v", root)
	}

	msgs := root["messages"].([]any)
	blocks := msgs[1].(map[string]any)["content"].([]any)

	thinking := blocks[0].(map[string]any)
	if _, present := thinking["signature"]; present {
		t.Error("signature was not removed from the thinking block")
	}
	if thinking["thinking"] != "reasoning here" {
		t.Error("thinking text must be preserved so reasoning context survives")
	}
	if blocks[1].(map[string]any)["data"] != "opaque" {
		t.Error("redacted_thinking block was altered")
	}
	if blocks[2].(map[string]any)["text"] != "answer" {
		t.Error("text block was altered")
	}
}

func TestStripThinkingSignaturesHandlesMultipleAndAbsent(t *testing.T) {
	multi := []byte(`{"messages":[
		{"role":"assistant","content":[{"type":"thinking","signature":"a"}]},
		{"role":"assistant","content":[{"type":"thinking","signature":"b"}]}
	]}`)
	if _, removed := stripThinkingSignatures(multi); removed != 2 {
		t.Errorf("removed = %d, want 2", removed)
	}

	none := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	out, removed := stripThinkingSignatures(none)
	if removed != 0 {
		t.Errorf("removed = %d, want 0", removed)
	}
	if string(out) != string(none) {
		t.Error("body must be returned untouched when there is nothing to strip")
	}
}

func TestStripThinkingSignaturesToleratesMalformedBodies(t *testing.T) {
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
		out, removed := stripThinkingSignatures(body)
		if removed != 0 {
			t.Errorf("%s: removed = %d, want 0", body, removed)
		}
		if string(out) != string(body) {
			t.Errorf("%s: body should be unchanged", body)
		}
	}
}

func TestRetryWithoutThinkingSignatures(t *testing.T) {
	body := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","signature":"stale"}]}]}`)

	repaired, ok := retryWithoutThinkingSignatures(body, []byte(realRejection), "/v1/messages")
	if !ok {
		t.Fatal("expected a retry for the observed rejection")
	}
	if string(repaired) == string(body) {
		t.Error("expected the retried body to differ from the original")
	}

	// An unrelated failure must not trigger a retry.
	if _, ok := retryWithoutThinkingSignatures(body, []byte(`{"error":{"message":"overloaded"}}`), "/v1/messages"); ok {
		t.Error("unrelated errors must not be retried")
	}

	// If the error names a signature but none exist, retrying would loop on an
	// identical request.
	clean := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	if _, ok := retryWithoutThinkingSignatures(clean, []byte(realRejection), "/v1/messages"); ok {
		t.Error("must not retry when there is no signature to strip")
	}
}
