package proxy

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestAnthropicModelDiscoveryAndPagination(t *testing.T) {
	raw := []byte(`{"data":[{"id":"gpt-6-astra","name":"GPT-6"},{"id":"claude-opus-5","display_name":"claude-opus-5 → gpt-6-astra","upstream_model":"gpt-6-astra"}]}`)
	for _, query := range []string{"", "?limit=1", "?limit=1&after_id=gpt-6-astra", "?before_id=claude-opus-5"} {
		w := httptest.NewRecorder()
		if e := writeAnthropicModels(w, httptest.NewRequest("GET", "/v1/models"+query, nil), raw); e != nil {
			t.Fatal(e)
		}
		var v map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &v)
		data := v["data"].([]any)
		if len(data) < 1 || data[0].(map[string]any)["type"] != "model" {
			t.Fatal(v)
		}
		if _, ok := v["has_more"]; !ok {
			t.Fatal("missing pagination")
		}
	}
	for _, query := range []string{"?limit=0", "?limit=bad", "?limit=1001", "?after_id=absent", "?after_id=gpt-6-astra&before_id=claude-opus-5"} {
		if e := writeAnthropicModels(httptest.NewRecorder(), httptest.NewRequest("GET", "/v1/models"+query, nil), raw); e == nil {
			t.Fatal(query)
		}
	}
	w := httptest.NewRecorder()
	if e := writeAnthropicModels(w, httptest.NewRequest("GET", "/v1/models", nil), []byte(`{"data":[]}`)); e != nil {
		t.Fatal(e)
	}
	var v map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &v)
	if v["first_id"] != nil || v["last_id"] != nil || v["has_more"] != false {
		t.Fatal(v)
	}
}
