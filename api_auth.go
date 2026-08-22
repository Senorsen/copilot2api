package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/whtsky/copilot2api/internal/reqctx"
)

// apiTokenSet maps secret tokens to their non-secret client IDs.
type apiTokenSet map[string]string

func parseAPITokenConfig(apiToken, apiTokens string) (apiTokenSet, error) {
	apiTokens = strings.TrimSpace(apiTokens)
	if apiToken != "" && apiTokens != "" {
		return nil, fmt.Errorf("API_TOKEN and API_TOKENS cannot both be configured")
	}
	if apiToken != "" {
		return apiTokenSet{apiToken: reqctx.DefaultClientID}, nil
	}
	if apiTokens == "" {
		return nil, nil
	}

	byToken := make(apiTokenSet)
	ids := make(map[string]struct{})
	for i, item := range strings.Fields(apiTokens) {
		id, token, ok := strings.Cut(item, ":")
		if !ok || id == "" || token == "" {
			return nil, fmt.Errorf("API_TOKENS entry %d must use non-empty id:token format", i+1)
		}
		if _, exists := ids[id]; exists {
			return nil, fmt.Errorf("duplicate API_TOKENS id %q", id)
		}
		if _, exists := byToken[token]; exists {
			return nil, fmt.Errorf("duplicate token in API_TOKENS entry %d", i+1)
		}
		ids[id] = struct{}{}
		byToken[token] = id
	}
	return byToken, nil
}

func apiTokenAuth(tokens apiTokenSet, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientID, ok := tokens.authenticate(r)
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid or missing API token","type":"authentication_error","code":"unauthorized"}}`))
			return
		}
		ctx := reqctx.WithClientID(r.Context(), clientID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (tokens apiTokenSet) authenticate(r *http.Request) (string, bool) {
	if authHeader := r.Header.Get("Authorization"); strings.HasPrefix(authHeader, "Bearer ") {
		if clientID, ok := tokens[strings.TrimPrefix(authHeader, "Bearer ")]; ok {
			return clientID, true
		}
	}
	if clientID, ok := tokens[r.Header.Get("x-api-key")]; ok {
		return clientID, true
	}
	return "", false
}
