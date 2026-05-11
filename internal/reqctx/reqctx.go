// Package reqctx provides request-scoped context helpers shared across handlers.
package reqctx

import (
	"context"
	"net/http"
	"strings"
)

type contextKey int

const (
	keyAffinity contextKey = iota
	keyAffinityHit
	keyHasCache
)

// WithAffinity stores gateway affinity info in the request context.
func WithAffinity(ctx context.Context, accountID string, hit bool, hasCache bool) context.Context {
	ctx = context.WithValue(ctx, keyAffinity, accountID)
	ctx = context.WithValue(ctx, keyAffinityHit, hit)
	ctx = context.WithValue(ctx, keyHasCache, hasCache)
	return ctx
}

// GetAffinity returns the affinity account ID, whether it was a cache hit,
// whether the request had cache_control, and whether this is a gateway request.
func GetAffinity(ctx context.Context) (accountID string, hit bool, hasCache bool, ok bool) {
	v, exists := ctx.Value(keyAffinity).(string)
	if !exists {
		return "", false, false, false
	}
	h, _ := ctx.Value(keyAffinityHit).(bool)
	c, _ := ctx.Value(keyHasCache).(bool)
	return v, h, c, true
}

// GetClientIP extracts the client IP from the request, checking
// X-Forwarded-For, X-Real-IP, and finally RemoteAddr.
func GetClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}
	addr := r.RemoteAddr
	if idx := strings.LastIndex(addr, ":"); idx != -1 {
		return addr[:idx]
	}
	return addr
}
