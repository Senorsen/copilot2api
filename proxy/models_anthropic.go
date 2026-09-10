package proxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

// Anthropic model discovery is selected by its version header (or explicit
// api_format=anthropic); existing OpenAI clients keep the upstream JSON shape.
func writeAnthropicModels(w http.ResponseWriter, r *http.Request, raw []byte) error {
	var source struct {
		Data []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			DisplayName string `json:"display_name"`
			Upstream    string `json:"upstream_model"`
		}
	}
	if err := json.Unmarshal(raw, &source); err != nil {
		return fmt.Errorf("invalid upstream model list")
	}
	type model struct {
		ID          string `json:"id"`
		Type        string `json:"type"`
		DisplayName string `json:"display_name"`
		CreatedAt   string `json:"created_at"`
		Upstream    string `json:"upstream_model,omitempty"`
	}
	data := make([]model, 0, len(source.Data))
	seen := map[string]bool{}
	for _, m := range source.Data {
		if m.ID == "" || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		name := m.DisplayName
		if name == "" {
			name = m.Name
		}
		if name == "" {
			name = m.ID
		}
		data = append(data, model{m.ID, "model", name, "1970-01-01T00:00:00Z", m.Upstream})
	}
	limit := 1000
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 1000 {
			return fmt.Errorf("limit must be 1..1000")
		}
		limit = n
	}
	after, before := r.URL.Query().Get("after_id"), r.URL.Query().Get("before_id")
	if after != "" && before != "" {
		return fmt.Errorf("use only one model cursor")
	}
	start, end := 0, len(data)
	if after != "" || before != "" {
		id := after
		if id == "" {
			id = before
		}
		found := -1
		for i, m := range data {
			if m.ID == id {
				found = i
				break
			}
		}
		if found < 0 {
			return fmt.Errorf("unknown model cursor")
		}
		if after != "" {
			start = found + 1
		} else {
			end = found
		}
	}
	more := end-start > limit
	if more {
		if before != "" {
			start = end - limit
		} else {
			end = start + limit
		}
	}
	page := data[start:end]
	var first, last *string
	if len(page) > 0 {
		first = &page[0].ID
		last = &page[len(page)-1].ID
	}
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(struct {
		Data    []model `json:"data"`
		HasMore bool    `json:"has_more"`
		FirstID *string `json:"first_id"`
		LastID  *string `json:"last_id"`
	}{page, more, first, last})
}
