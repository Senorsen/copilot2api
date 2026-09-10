package anthropic

import "encoding/json"

// Content blocks are a tagged union: fields required by one variant must still
// be emitted when empty, without adding those fields to unrelated variants.
// In particular, SDK stream consumers initialize their state from block_start.
func (b AnthropicContentBlock) MarshalJSON() ([]byte, error) {
	type fields AnthropicContentBlock
	switch b.Type {
	case "text":
		return json.Marshal(struct {
			fields
			Text string `json:"text"`
		}{fields: fields(b), Text: b.Text})
	case "thinking":
		return json.Marshal(struct {
			fields
			Thinking  string `json:"thinking"`
			Signature string `json:"signature"`
		}{fields: fields(b), Thinking: b.Thinking, Signature: b.Signature})
	case "tool_use":
		input := b.Input
		if input == nil {
			input = map[string]interface{}{}
		}
		return json.Marshal(struct {
			fields
			Input map[string]interface{} `json:"input"`
		}{fields: fields(b), Input: input})
	default:
		return json.Marshal(fields(b))
	}
}

// Empty delta strings are values, not absent fields. Omitting thinking from an
// empty thinking_delta passes undefined to SDK callbacks (Office reads length).
func (d AnthropicContentDelta) MarshalJSON() ([]byte, error) {
	type fields AnthropicContentDelta
	switch d.Type {
	case "text_delta":
		return json.Marshal(struct {
			fields
			Text string `json:"text"`
		}{fields: fields(d), Text: d.Text})
	case "thinking_delta":
		return json.Marshal(struct {
			fields
			Thinking string `json:"thinking"`
		}{fields: fields(d), Thinking: d.Thinking})
	case "signature_delta":
		return json.Marshal(struct {
			fields
			Signature string `json:"signature"`
		}{fields: fields(d), Signature: d.Signature})
	case "input_json_delta":
		return json.Marshal(struct {
			fields
			PartialJSON string `json:"partial_json"`
		}{fields: fields(d), PartialJSON: d.PartialJSON})
	default:
		return json.Marshal(fields(d))
	}
}
