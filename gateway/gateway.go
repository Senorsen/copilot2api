package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/whtsky/copilot2api/anthropic"
	"github.com/whtsky/copilot2api/auth"
	"github.com/whtsky/copilot2api/internal/models"
	"github.com/whtsky/copilot2api/internal/reqctx"
	"github.com/whtsky/copilot2api/proxy"
)

// affinityEntry stores the last account used for a given IP+model combo.
type affinityEntry struct {
	AccountID string
	HasCache  bool // for Anthropic: whether the previous request had cache_control
	ExpiresAt time.Time
}

// Handler implements the /gw/api/... gateway with load balancing and cache affinity.
type Handler struct {
	am        *auth.AccountManager
	transport *http.Transport
	mc        *models.Cache
	exclude   map[string]bool

	mu       sync.RWMutex
	affinity map[string]*affinityEntry // key: ip + "|" + model
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
		am:        am,
		transport: transport,
		mc:        mc,
		exclude:   exclude,
		affinity:  make(map[string]*affinityEntry),
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

	// Determine account via affinity or random
	affinityHit := false
	var chosenAccountID string

	affinityKey := clientIP + "|" + model
	if model != "" {
		h.mu.RLock()
		entry, exists := h.affinity[affinityKey]
		h.mu.RUnlock()

		if exists && time.Now().Before(entry.ExpiresAt) {
			isAnthropic := strings.Contains(remainder, "/v1/messages")
			if isAnthropic {
				// Only use affinity if both previous and current have cache_control
				if entry.HasCache && hasCache {
					if h.inPool(entry.AccountID, pool) {
						chosenAccountID = entry.AccountID
						affinityHit = true
					}
				}
			} else {
				// OpenAI: always use affinity
				if h.inPool(entry.AccountID, pool) {
					chosenAccountID = entry.AccountID
					affinityHit = true
				}
			}
		}
	}

	if chosenAccountID == "" {
		chosenAccountID = pool[rand.Intn(len(pool))]
	}

	// Try to get client and validate token before dispatching
	client, ok := h.am.GetClient(chosenAccountID)
	if !ok || !h.canGetToken(client) {
		// Retry with another account
		retryID := h.pickOther(pool, chosenAccountID)
		if retryID != "" {
			chosenAccountID = retryID
			affinityHit = false
			client, ok = h.am.GetClient(chosenAccountID)
		}
		if !ok {
			proxy.WriteOpenAIError(w, http.StatusBadGateway, proxy.OpenAIErrorTypeServerError, "no available account")
			return
		}
	}

	tp := auth.NewAccountTokenProvider(client)
	tp.AccountID = chosenAccountID

	// Update affinity record
	if model != "" {
		h.mu.Lock()
		h.affinity[affinityKey] = &affinityEntry{
			AccountID: chosenAccountID,
			HasCache:  hasCache,
			ExpiresAt: time.Now().Add(5 * time.Minute),
		}
		h.mu.Unlock()
	}

	// Inject affinity info into request context for downstream handlers to log.
	ctx := reqctx.WithAffinity(r.Context(), chosenAccountID, affinityHit, hasCache)
	r = r.WithContext(ctx)

	// Restore body and rewrite path
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	r.URL.Path = remainder

	switch {
	case remainder == "/v1/messages" || strings.HasPrefix(remainder, "/v1/messages"):
		handler := anthropic.NewHandler(tp, h.transport, h.mc)
		handler.ServeHTTP(w, r)
	default:
		handler := proxy.NewHandler(tp, h.transport, h.mc, nil)
		handler.ServeHTTP(w, r)
	}
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

func (h *Handler) pickOther(pool []string, exclude string) string {
	var others []string
	for _, p := range pool {
		if p != exclude {
			others = append(others, p)
		}
	}
	if len(others) == 0 {
		return ""
	}
	return others[rand.Intn(len(others))]
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
