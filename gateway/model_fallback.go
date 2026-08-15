package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/whtsky/copilot2api/anthropic"
	"github.com/whtsky/copilot2api/auth"
	"github.com/whtsky/copilot2api/internal/reqctx"
	"github.com/whtsky/copilot2api/internal/upstream"
	"github.com/whtsky/copilot2api/proxy"
)

const accountModelsTTL = 5 * time.Minute

type accountModelsEntry struct {
	models    map[string]struct{}
	expiresAt time.Time
}

type accountModelResult struct {
	accountID string
	supported bool
	err       error
}

// filterPoolForModel resolves account-specific /models lists before choosing an
// account. This prevents the gateway from randomly selecting an account that
// does not currently advertise the requested model.
func (h *Handler) filterPoolForModel(ctx context.Context, pool []string, model string) ([]string, bool) {
	if model == "" || len(pool) == 0 {
		return pool, false
	}

	results := make(chan accountModelResult, len(pool))
	var wg sync.WaitGroup
	for _, accountID := range pool {
		wg.Add(1)
		go func() {
			defer wg.Done()
			supported, err := h.accountHasModel(ctx, accountID, model)
			results <- accountModelResult{accountID: accountID, supported: supported, err: err}
		}()
	}
	wg.Wait()
	close(results)

	available := make([]string, 0, len(pool))
	unknown := make([]string, 0, len(pool))
	for result := range results {
		switch {
		case result.err != nil:
			unknown = append(unknown, result.accountID)
			slog.Warn("gateway model availability check failed", "account_id", result.accountID, "model", model, "error", result.err)
		case result.supported:
			available = append(available, result.accountID)
		}
	}

	if len(available) > 0 {
		return available, false
	}
	if len(unknown) > 0 {
		// Preserve service during a transient /models failure. Reactive retry
		// below still moves away from an account that rejects the model.
		return unknown, false
	}
	return nil, true
}

func (h *Handler) accountHasModel(ctx context.Context, accountID, model string) (bool, error) {
	now := time.Now()
	h.modelsMu.RLock()
	entry := h.accountModels[accountID]
	if entry != nil && now.Before(entry.expiresAt) {
		_, ok := entry.models[model]
		h.modelsMu.RUnlock()
		return ok, nil
	}
	h.modelsMu.RUnlock()

	client, ok := h.am.GetClient(accountID)
	if !ok {
		return false, context.Canceled
	}
	tp := auth.NewAccountTokenProvider(client)
	tp.AccountID = accountID
	upstreamClient := upstream.NewClient(tp, h.transport)

	fetchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, body, err := upstreamClient.Do(fetchCtx, upstream.Request{Method: http.MethodGet, Endpoint: "/models"})
	if err != nil {
		return false, err
	}

	var response struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return false, err
	}

	modelSet := make(map[string]struct{}, len(response.Data))
	for _, item := range response.Data {
		if item.ID != "" {
			modelSet[item.ID] = struct{}{}
		}
	}
	h.modelsMu.Lock()
	h.accountModels[accountID] = &accountModelsEntry{models: modelSet, expiresAt: time.Now().Add(accountModelsTTL)}
	h.modelsMu.Unlock()

	_, supported := modelSet[model]
	return supported, nil
}

func (h *Handler) invalidateAccountModels(accountID string) {
	h.modelsMu.Lock()
	delete(h.accountModels, accountID)
	h.modelsMu.Unlock()
}

func (h *Handler) serveWithAccountFallback(
	w http.ResponseWriter,
	r *http.Request,
	remainder string,
	bodyBytes []byte,
	model string,
	hasCache bool,
	clientIP string,
	pool []string,
) {
	isAnthropic := strings.Contains(remainder, "/v1/messages")
	affinityKey := clientIP + "|" + model
	affinityAccount := ""
	if model != "" {
		h.mu.RLock()
		entry := h.affinity[affinityKey]
		h.mu.RUnlock()
		if entry != nil && time.Now().Before(entry.ExpiresAt) && h.inPool(entry.AccountID, pool) {
			if !isAnthropic || hasCache {
				affinityAccount = entry.AccountID
			}
		}
	}

	attemptOrder := append([]string(nil), pool...)
	rand.Shuffle(len(attemptOrder), func(i, j int) {
		attemptOrder[i], attemptOrder[j] = attemptOrder[j], attemptOrder[i]
	})
	if affinityAccount != "" {
		for i, id := range attemptOrder {
			if id == affinityAccount {
				attemptOrder[0], attemptOrder[i] = attemptOrder[i], attemptOrder[0]
				break
			}
		}
	}

	var lastUnsupported *deferredResponseWriter
	for _, accountID := range attemptOrder {
		client, ok := h.am.GetClient(accountID)
		if !ok || !h.canGetToken(client) {
			continue
		}

		tp := auth.NewAccountTokenProvider(client)
		tp.AccountID = accountID
		affinityHit := affinityAccount != "" && accountID == affinityAccount
		ctx := reqctx.WithAffinity(r.Context(), accountID, affinityHit, hasCache)
		attemptRequest := r.Clone(ctx)
		attemptURL := *r.URL
		attemptURL.Path = remainder
		attemptRequest.URL = &attemptURL
		attemptRequest.Body = io.NopCloser(bytes.NewReader(bodyBytes))

		attemptWriter := newDeferredResponseWriter(w)
		switch {
		case remainder == "/v1/messages" || strings.HasPrefix(remainder, "/v1/messages"):
			handler := anthropic.NewHandler(tp, h.transport, h.mc)
			handler.StatsRecorder = h.Recorder
			handler.ServeHTTP(attemptWriter, attemptRequest)
		default:
			handler := proxy.NewHandler(tp, h.transport, h.mc, nil)
			handler.StatsRecorder = h.Recorder
			handler.ServeHTTP(attemptWriter, attemptRequest)
		}

		if attemptWriter.committed {
			h.updateAffinity(affinityKey, model, isAnthropic, hasCache, accountID)
			return
		}
		if isRequestedModelUnsupported(attemptWriter.statusCode(), attemptWriter.body.Bytes()) {
			h.invalidateAccountModels(accountID)
			lastUnsupported = attemptWriter
			slog.Warn("gateway account rejected requested model; trying another account", "account_id", accountID, "model", model)
			continue
		}

		h.updateAffinity(affinityKey, model, isAnthropic, hasCache, accountID)
		attemptWriter.flushCaptured()
		return
	}

	if lastUnsupported != nil {
		lastUnsupported.flushCaptured()
		return
	}
	proxy.WriteOpenAIError(w, http.StatusBadGateway, proxy.OpenAIErrorTypeServerError, "no available account")
}

func (h *Handler) updateAffinity(key, model string, isAnthropic, hasCache bool, accountID string) {
	if model == "" || (isAnthropic && !hasCache) {
		return
	}
	h.mu.Lock()
	h.affinity[key] = &affinityEntry{AccountID: accountID, ExpiresAt: time.Now().Add(time.Hour)}
	h.mu.Unlock()
}

func isRequestedModelUnsupported(status int, body []byte) bool {
	if status != http.StatusBadRequest {
		return false
	}
	return strings.Contains(strings.ToLower(string(body)), "the requested model is not supported")
}

// deferredResponseWriter passes successful responses through immediately so SSE
// streaming is unaffected, while retaining error responses long enough for the
// gateway to decide whether another account should be tried.
type deferredResponseWriter struct {
	dst       http.ResponseWriter
	header    http.Header
	status    int
	body      bytes.Buffer
	committed bool
}

func newDeferredResponseWriter(dst http.ResponseWriter) *deferredResponseWriter {
	return &deferredResponseWriter{dst: dst, header: make(http.Header)}
}

func (w *deferredResponseWriter) Header() http.Header { return w.header }

func (w *deferredResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	if status < http.StatusBadRequest {
		w.commit()
	}
}

func (w *deferredResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.committed {
		return w.dst.Write(p)
	}
	return w.body.Write(p)
}

func (w *deferredResponseWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.committed {
		if flusher, ok := w.dst.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}

func (w *deferredResponseWriter) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (w *deferredResponseWriter) commit() {
	if w.committed {
		return
	}
	copyHeaders(w.dst.Header(), w.header)
	w.dst.WriteHeader(w.statusCode())
	w.committed = true
}

func (w *deferredResponseWriter) flushCaptured() {
	if !w.committed {
		w.commit()
	}
	if w.body.Len() > 0 {
		_, _ = w.dst.Write(w.body.Bytes())
	}
}

func copyHeaders(dst, src http.Header) {
	for key := range dst {
		dst.Del(key)
	}
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}
