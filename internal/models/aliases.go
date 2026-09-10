package models

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/whtsky/copilot2api/internal/upstream"
)

// Aliases is parsed once at startup and copied into the cache. Exact keys win
// over built-in Anthropic spelling normalization; targets are literal IDs.
type Aliases map[string]string

func ParseAliases(value string) (Aliases, error) {
	out := Aliases{}
	if strings.TrimSpace(value) == "" {
		return out, nil
	}
	d := json.NewDecoder(strings.NewReader(value))
	t, e := d.Token()
	if e != nil || t != json.Delim('{') {
		return nil, fmt.Errorf("model aliases must be a JSON object")
	}
	valid := func(s string) bool {
		return s != "" && len(s) <= 256 && !strings.ContainsAny(s, "/\\\"\r\n") && !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
	}
	for d.More() {
		t, e = d.Token()
		if e != nil {
			return nil, e
		}
		key, ok := t.(string)
		if !ok || !valid(key) {
			return nil, fmt.Errorf("invalid model alias ID")
		}
		if _, ok = out[key]; ok {
			return nil, fmt.Errorf("duplicate model alias %q", key)
		}
		var target string
		if e = d.Decode(&target); e != nil || !valid(target) {
			return nil, fmt.Errorf("invalid target for model alias %q", key)
		}
		out[key] = target
	}
	if _, e = d.Token(); e != nil {
		return nil, e
	}
	if _, e = d.Token(); e != io.EOF {
		return nil, fmt.Errorf("trailing model alias configuration")
	}
	for k, v := range out {
		if _, ok := out[v]; ok && v != k {
			return nil, fmt.Errorf("model alias chains are not allowed: %q targets another alias", k)
		}
	}
	return out, nil
}
func (c *Cache) ResolveAlias(id string) (string, bool) {
	if c != nil {
		if target, ok := c.aliases[id]; ok {
			return target, true
		}
	}
	return id, false
}

// AddAliases preserves upstream metadata, and only advertises aliases whose
// target is present. The display name makes cross-provider remapping explicit.
func (c *Cache) AddAliases(raw []byte) ([]byte, error) {
	if c == nil || len(c.aliases) == 0 {
		return raw, nil
	}
	var list map[string]json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	if list == nil {
		return nil, fmt.Errorf("invalid upstream model list")
	}
	var data []map[string]json.RawMessage
	if err := json.Unmarshal(list["data"], &data); err != nil {
		return nil, err
	}
	targets := map[string]map[string]json.RawMessage{}
	for _, m := range data {
		var id string
		_ = json.Unmarshal(m["id"], &id)
		targets[id] = m
	}
	result := make([]map[string]json.RawMessage, 0, len(data)+len(c.aliases))
	for _, m := range data {
		var id string
		_ = json.Unmarshal(m["id"], &id)
		if _, replace := c.aliases[id]; !replace {
			result = append(result, m)
		}
	}
	keys := make([]string, 0, len(c.aliases))
	for k := range c.aliases {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, alias := range keys {
		target := c.aliases[alias]
		m, ok := targets[target]
		if !ok {
			continue
		}
		copy := map[string]json.RawMessage{}
		for k, v := range m {
			copy[k] = bytes.Clone(v)
		}
		copy["id"], _ = json.Marshal(alias)
		copy["name"], _ = json.Marshal(alias + " → " + target)
		copy["display_name"] = copy["name"]
		copy["upstream_model"], _ = json.Marshal(target)
		result = append(result, copy)
	}
	list["data"], _ = json.Marshal(result)
	return json.Marshal(list)
}

func NewCacheWithAliases(provider func() *upstream.Client, ttl time.Duration, aliases Aliases) *Cache {
	c := NewCache(provider, ttl)
	c.aliases = make(Aliases, len(aliases))
	for k, v := range aliases {
		c.aliases[k] = v
	}
	return c
}

// ForProvider prevents model discovery/capability selection for a direct account
// from accidentally using the first (possibly different-provider) account.
func (c *Cache) ForProvider(tp upstream.TokenProvider, transport *http.Transport) *Cache {
	if c == nil {
		return nil
	}
	p, ok := tp.(upstream.AccountInfoProvider)
	if !ok {
		return c
	}
	id, _ := p.GetAccountInfo()
	if id == "" {
		return c
	}
	c.scopesMu.Lock()
	defer c.scopesMu.Unlock()
	if c.scopes == nil {
		c.scopes = map[string]*Cache{}
	}
	if v := c.scopes[id]; v != nil {
		return v
	}
	child := NewCacheWithAliases(func() *upstream.Client { return upstream.NewClient(tp, transport) }, c.ttl, c.aliases)
	c.scopes[id] = child
	return child
}
