package anthropic

import (
	"encoding/json"
	"testing"
)

// decode is a small helper so the fixtures below stay readable as JSON.
func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("bad fixture: %v", err)
	}
	return m
}

func contentTypes(t *testing.T, obj map[string]any, msgIdx int) []string {
	t.Helper()
	msgs := obj["messages"].([]any)
	blocks := msgs[msgIdx].(map[string]any)["content"].([]any)
	out := make([]string, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, b.(map[string]any)["type"].(string))
	}
	return out
}

// This mirrors the shape observed in the Claude Code session captured on
// 2026-08-07: a history turn holding signature-only thinking blocks alongside
// real content, which is what upstream rejected.
func TestDropsSignatureOnlyThinkingFromHistory(t *testing.T) {
	obj := decode(t, `{
		"messages": [
			{"role":"user","content":[{"type":"text","text":"hi"}]},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"","signature":"CAISoAgKhwEIEBgC"},
				{"type":"thinking","thinking":"","signature":"CAISoAgKhwEIEBgD"},
				{"type":"text","text":"answer"},
				{"type":"tool_use","id":"t1","name":"Read","input":{}}
			]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1"}]}
		]
	}`)

	stripEmptyThinkingBlocks(obj)

	got := contentTypes(t, obj, 1)
	want := []string{"text", "tool_use"}
	if len(got) != len(want) {
		t.Fatalf("content = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("content = %v, want %v", got, want)
		}
	}
}

func TestKeepsThinkingThatCarriesText(t *testing.T) {
	// Reasoning text does not exempt a block: its signature is verified just the
	// same, and leaving it behind reproduces the failure this guards against.
	obj := decode(t, `{
		"messages": [
			{"role":"user","content":[{"type":"text","text":"hi"}]},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"I'm planning to add a check","signature":"sig"},
				{"type":"text","text":"answer"}
			]},
			{"role":"user","content":[{"type":"text","text":"next"}]}
		]
	}`)

	stripEmptyThinkingBlocks(obj)

	if got := contentTypes(t, obj, 1); len(got) != 1 || got[0] != "text" {
		t.Errorf("content = %v, want the signed thinking block dropped", got)
	}
}

func TestKeepsUnsignedAndRedactedThinking(t *testing.T) {
	// Without a signature there is nothing for upstream to reject, and
	// redacted_thinking is a different block type we must not touch.
	obj := decode(t, `{
		"messages": [
			{"role":"user","content":[{"type":"text","text":"hi"}]},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":""},
				{"type":"redacted_thinking","data":"opaque"},
				{"type":"text","text":"answer"}
			]},
			{"role":"user","content":[{"type":"text","text":"next"}]}
		]
	}`)

	stripEmptyThinkingBlocks(obj)

	if got := contentTypes(t, obj, 1); len(got) != 3 {
		t.Errorf("content = %v, want all three blocks preserved", got)
	}
}

func TestProtectsTrailingAssistantTurn(t *testing.T) {
	// With thinking enabled the final assistant message must still start with a
	// thinking block, so stripping there would swap one rejection for another.
	obj := decode(t, `{
		"messages": [
			{"role":"user","content":[{"type":"text","text":"hi"}]},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"","signature":"sig"},
				{"type":"tool_use","id":"t1","name":"Read","input":{}}
			]}
		]
	}`)

	stripEmptyThinkingBlocks(obj)

	if got := contentTypes(t, obj, 1); len(got) != 2 || got[0] != "thinking" {
		t.Errorf("content = %v, want the trailing turn untouched", got)
	}
}

func TestKeepsSoleBlock(t *testing.T) {
	// Removing it would leave an empty, invalid message.
	obj := decode(t, `{
		"messages": [
			{"role":"user","content":[{"type":"text","text":"hi"}]},
			{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"sig"}]},
			{"role":"user","content":[{"type":"text","text":"next"}]}
		]
	}`)

	stripEmptyThinkingBlocks(obj)

	if got := contentTypes(t, obj, 1); len(got) != 1 {
		t.Errorf("content = %v, want the sole block kept", got)
	}
}

func TestToleratesUnexpectedShapes(t *testing.T) {
	// Malformed or unusual bodies must not panic; content is legitimately a
	// plain string in some clients.
	for _, fixture := range []string{
		`{}`,
		`{"messages":"oops"}`,
		`{"messages":[{"role":"user","content":"plain string"}]}`,
		`{"messages":[null]}`,
	} {
		obj := decode(t, fixture)
		stripEmptyThinkingBlocks(obj)
	}
}

func TestNormalizeAppliesTheStrip(t *testing.T) {
	// The strip has to run on the real passthrough path, not just in isolation.
	body := []byte(`{
		"model":"claude-opus-5",
		"messages":[
			{"role":"user","content":[{"type":"text","text":"hi"}]},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"","signature":"stale"},
				{"type":"text","text":"answer"}
			]},
			{"role":"user","content":[{"type":"text","text":"next"}]}
		]
	}`)

	out, err := normalizeNativeMessagesBody(body, "claude-opus-5", false)
	if err != nil {
		t.Fatal(err)
	}

	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatal(err)
	}
	if got := contentTypes(t, obj, 1); len(got) != 1 || got[0] != "text" {
		t.Errorf("content = %v, want only the text block", got)
	}
}
