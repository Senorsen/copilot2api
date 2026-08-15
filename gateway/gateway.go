package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/whtsky/copilot2api/auth"
	"github.com/whtsky/copilot2api/internal/models"
	"github.com/whtsky/copilot2api/internal/reqctx"
	"github.com/whtsky/copilot2api/proxy"
	"github.com/whtsky/copilot2api/stats"
)

// affinityEntry stores the last account used for a given IP+model combo.
type affinityEntry struct {
	AccountID string
	ExpiresAt time.Time
}

// Handler implements the /gw/api/... gateway with load balancing and cache affinity.
type Handler struct {
	am        *auth.AccountManager
	transport *http.Transport
	mc        *models.Cache
	exclude   map[string]bool
	Recorder  *stats.Recorder

	mu       sync.RWMutex
	affinity map[string]*affinityEntry // key: ip + "|" + model

	modelsMu      sync.RWMutex
	accountModels map[string]*accountModelsEntry
}

// NewHandler creates a new gateway handler.
func NewHandler(am *auth.AccountManager, transport *http.Transport, mc *models.Cache) *Handler {
	exclude := make(map[string]bool)
	if v := os.Getenv("GW_EXCLUDE"); v != "" {
		for _, id := range strings.Split(v, ",") {
			id = strings.TrimSpace(id)
			if id != "" {
				exclude[id] = true
			}
		}
	}
	h := &Handler{
		am:            am,
		transport:     transport,
		mc:            mc,
		exclude:       exclude,
		affinity:      make(map[string]*affinityEntry),
		accountModels: make(map[string]*accountModelsEntry),
	}
	go h.cleanupLoop()
	return h
}

// cleanupLoop periodically removes expired affinity entries to prevent unbounded map growth.
func (h *Handler) cleanupLoop() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		h.mu.Lock()
		for k, v := range h.affinity {
			if now.After(v.ExpiresAt) {
				delete(h.affinity, k)
			}
		}
		h.mu.Unlock()
	}
}

// ServeHTTP handles /gw/api/... requests.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Strip /gw/api prefix to get the remainder like /v1/messages
	remainder := strings.TrimPrefix(r.URL.Path, "/gw/api")
	if remainder == "" || remainder[0] != '/' {
		remainder = "/" + remainder
	}

	clientIP := reqctx.GetClientIP(r)

	// Read body once for model/cache extraction, then restore it
	var bodyBytes []byte
	if r.Body != nil {
		var err error
		bodyBytes, err = io.ReadAll(r.Body)
		if err != nil {
			proxy.WriteOpenAIError(w, http.StatusBadRequest, proxy.OpenAIErrorTypeInvalidRequest, "failed to read request body")
			return
		}
	}

	model, hasCache := extractModelAndCache(bodyBytes, remainder)

	// Get available accounts pool
	pool := h.getPool()
	if len(pool) == 0 {
		proxy.WriteOpenAIError(w, http.StatusServiceUnavailable, proxy.OpenAIErrorTypeServerError, "no accounts available")
		return
	}

	filteredPool, knownUnsupported := h.filterPoolForModel(r.Context(), pool, model)
	if len(filteredPool) == 0 {
		if knownUnsupported {
			proxy.WriteOpenAIError(w, http.StatusBadRequest, proxy.OpenAIErrorTypeInvalidRequest, "The requested model is not supported by any available account.")
		} else {
			proxy.WriteOpenAIError(w, http.StatusServiceUnavailable, proxy.OpenAIErrorTypeServerError, "no accounts available")
		}
		return
	}

	h.serveWithAccountFallback(w, r, remainder, bodyBytes, model, hasCache, clientIP, filteredPool)
}

func (h *Handler) canGetToken(client *auth.Client) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := client.GetToken(ctx)
	return err == nil
}

func (h *Handler) getPool() []string {
	accounts := h.am.ListAccounts()
	var pool []string
	for _, acc := range accounts {
		if !h.exclude[acc.ID] {
			pool = append(pool, acc.ID)
		}
	}
	return pool
}

func (h *Handler) inPool(id string, pool []string) bool {
	for _, p := range pool {
		if p == id {
			return true
		}
	}
	return false
}

func extractModelAndCache(body []byte, remainder string) (model string, hasCache bool) {
	if len(body) == 0 {
		return "", false
	}
	var parsed struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil {
		model = parsed.Model
	}
	if strings.Contains(remainder, "/v1/messages") {
		hasCache = bytes.Contains(body, []byte(`"cache_control"`))
	}
	return model, hasCache
}
