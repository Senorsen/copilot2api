package models

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseAliases(t *testing.T) {
	for _, s := range []string{`[]`, `{"a":"b","a":"c"}`, `{"a":null}`, `{"a":""}`, `{"a":"b","b":"c"}`, `{"a":"b","b":"a"}`, `{"a b":"c"}`, `{"a":"b"} trailing`} {
		if _, e := ParseAliases(s); e == nil {
			t.Errorf("accepted %s", s)
		}
	}
	a, e := ParseAliases(`{"claude-opus-5":"gpt-6-astra","claude-opus-5[1m]":"gpt-6-astra","other":"gpt-5.6-sol"}`)
	if e != nil || len(a) != 3 {
		t.Fatal(a, e)
	}
	c := NewCacheWithAliases(nil, time.Minute, a)
	a["claude-opus-5"] = "mutated"
	if got, ok := c.ResolveAlias("claude-opus-5"); !ok || got != "gpt-6-astra" {
		t.Fatal(got, ok)
	}
	if got, ok := c.ResolveAlias("CLAUDE-OPUS-5"); ok || got != "CLAUDE-OPUS-5" {
		t.Fatal("case must be exact")
	}
	var missing *Cache
	if got, ok := missing.ResolveAlias("x"); got != "x" || ok {
		t.Fatal("nil cache")
	}
}
func TestAliasDiscoveryPreservesRealMetadata(t *testing.T) {
	a, _ := ParseAliases(`{"claude-opus-5":"gpt-6-astra","claude-opus-5[1m]":"gpt-6-astra","not-available":"absent"}`)
	c := NewCacheWithAliases(nil, time.Minute, a)
	raw := []byte(`{"object":"list","data":[{"id":"gpt-6-astra","name":"GPT-6","capabilities":{"limits":{"max_context_window_tokens":500000}},"supported_endpoints":["/responses"]},{"id":"claude-opus-5","name":"native collision"}]}`)
	out, e := c.AddAliases(raw)
	if e != nil {
		t.Fatal(e)
	}
	var d struct{ Data []map[string]any }
	if e = json.Unmarshal(out, &d); e != nil {
		t.Fatal(e)
	}
	if len(d.Data) != 3 {
		t.Fatalf("data %s", out)
	}
	for _, m := range d.Data {
		if m["id"] == "not-available" {
			t.Fatal("advertised unavailable target")
		}
		if m["id"] != "gpt-6-astra" {
			if m["upstream_model"] != "gpt-6-astra" {
				t.Fatal(m)
			}
			if _, ok := m["capabilities"]; !ok {
				t.Fatal("capabilities dropped")
			}
		}
	}
	if string(raw) != `{"object":"list","data":[{"id":"gpt-6-astra","name":"GPT-6","capabilities":{"limits":{"max_context_window_tokens":500000}},"supported_endpoints":["/responses"]},{"id":"claude-opus-5","name":"native collision"}]}` {
		t.Fatal("mutated raw cache")
	}
}

type scopedAliasProvider struct{ id, url string }

func (p *scopedAliasProvider) GetToken(context.Context) (string, error) { return "test-token", nil }
func (p *scopedAliasProvider) GetBaseURL() string                       { return p.url }
func (p *scopedAliasProvider) GetAccountInfo() (string, string)         { return p.id, p.id }
func TestPerAccountAliasCatalogIsolation(t *testing.T) {
	server := func(id string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": id}}})
		}))
	}
	a, b := server("gpt-6-astra"), server("other")
	defer a.Close()
	defer b.Close()
	c := NewCacheWithAliases(nil, time.Minute, Aliases{"claude-opus-5": "gpt-6-astra"})
	pa, pb := &scopedAliasProvider{"a", a.URL}, &scopedAliasProvider{"b", b.URL}
	ca, cb := c.ForProvider(pa, nil), c.ForProvider(pb, nil)
	if ca == cb || ca != c.ForProvider(pa, nil) {
		t.Fatal("cache scopes")
	}
	for i, cache := range []*Cache{ca, cb} {
		raw, e := cache.GetRaw(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		raw, e = cache.AddAliases(raw)
		if e != nil {
			t.Fatal(e)
		}
		has := strings.Contains(string(raw), "claude-opus-5")
		if has != (i == 0) {
			t.Fatalf("cross-account alias advertisement %s", raw)
		}
	}
}

func TestAliasDiscoveryInvalidUpstream(t *testing.T) {
	c := NewCacheWithAliases(nil, time.Minute, Aliases{"a": "b"})
	for _, raw := range []string{"null", "[]", "{", `{"data":false}`} {
		if _, err := c.AddAliases([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
