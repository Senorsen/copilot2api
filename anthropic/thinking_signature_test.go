package anthropic

import (
	"bytes"
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

	out, removed := stripThinkingSignatures(body, -1)
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

func TestOffendingMessageIndex(t *testing.T) {
	if got := offendingMessageIndex([]byte(realRejection)); got != 1 {
		t.Errorf("index = %d, want 1 (from messages.1.content.37)", got)
	}
	if got := offendingMessageIndex([]byte(`{"error":{"message":"messages.12.content.3: Invalid ` + "`signature`" + `"}}`)); got != 12 {
		t.Errorf("index = %d, want 12", got)
	}
	if got := offendingMessageIndex([]byte(`{"error":{"message":"something went wrong"}}`)); got != -1 {
		t.Errorf("index = %d, want -1 when no message is named", got)
	}
}

func TestStripThinkingSignaturesScopedToOneMessage(t *testing.T) {
	// Signatures on turns the upstream did not complain about must survive, so
	// unrelated reasoning context is not discarded.
	body := []byte(`{"messages":[
		{"role":"assistant","content":[{"type":"thinking","signature":"keep-0"}]},
		{"role":"assistant","content":[{"type":"thinking","signature":"drop-1"}]},
		{"role":"assistant","content":[{"type":"thinking","signature":"keep-2"}]}
	]}`)

	out, removed := stripThinkingSignatures(body, 1)
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}

	var root map[string]any
	if err := json.Unmarshal(out, &root); err != nil {
		t.Fatal(err)
	}
	msgs := root["messages"].([]any)

	sig := func(i int) any {
		blocks := msgs[i].(map[string]any)["content"].([]any)
		return blocks[0].(map[string]any)["signature"]
	}
	if sig(0) != "keep-0" || sig(2) != "keep-2" {
		t.Error("signatures on unaffected messages must be preserved")
	}
	if sig(1) != nil {
		t.Error("signature on the named message should have been removed")
	}
}

func TestRetryScopesToNamedMessageThenWidens(t *testing.T) {
	// The named message carries the bad signature: only it should be touched.
	scoped := []byte(`{"messages":[
		{"role":"assistant","content":[{"type":"thinking","signature":"keep"}]},
		{"role":"assistant","content":[{"type":"thinking","signature":"bad"}]}
	]}`)
	repaired, ok := retryWithoutThinkingSignatures(scoped, []byte(realRejection), "/v1/messages")
	if !ok {
		t.Fatal("expected a retry")
	}
	if !bytes.Contains(repaired, []byte("keep")) {
		t.Error("signature outside the named message should be preserved")
	}
	if bytes.Contains(repaired, []byte("bad")) {
		t.Error("signature in the named message should be removed")
	}

	// The named message holds no signature (e.g. indices shifted): fall back to
	// clearing everything rather than replaying an identical request.
	shifted := []byte(`{"messages":[
		{"role":"assistant","content":[{"type":"thinking","signature":"only-here"}]},
		{"role":"user","content":[{"type":"text","text":"hi"}]}
	]}`)
	repaired, ok = retryWithoutThinkingSignatures(shifted, []byte(realRejection), "/v1/messages")
	if !ok {
		t.Fatal("expected a fallback retry")
	}
	if bytes.Contains(repaired, []byte("only-here")) {
		t.Error("fallback should have cleared every signature")
	}
}

func TestStripThinkingSignaturesHandlesMultipleAndAbsent(t *testing.T) {
	multi := []byte(`{"messages":[
		{"role":"assistant","content":[{"type":"thinking","signature":"a"}]},
		{"role":"assistant","content":[{"type":"thinking","signature":"b"}]}
	]}`)
	if _, removed := stripThinkingSignatures(multi, -1); removed != 2 {
		t.Errorf("removed = %d, want 2", removed)
	}

	none := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	out, removed := stripThinkingSignatures(none, -1)
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
		out, removed := stripThinkingSignatures(body, -1)
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
