package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/whtsky/copilot2api/internal/reqctx"
	"github.com/whtsky/copilot2api/internal/securetoken"
)

type apiTokenCredential struct {
	clientID string
	digest   securetoken.Digest
}

// apiTokenSet stores pre-hashed tokens and their non-secret client IDs.
type apiTokenSet []apiTokenCredential

func parseAPITokenConfig(apiToken, apiTokens string) (apiTokenSet, error) {
	apiTokens = strings.TrimSpace(apiTokens)
	if apiToken != "" && apiTokens != "" {
		return nil, fmt.Errorf("API_TOKEN and API_TOKENS cannot both be configured")
	}
	if apiToken != "" {
		return apiTokenSet{{clientID: reqctx.DefaultClientID, digest: securetoken.Hash(apiToken)}}, nil
	}
	if apiTokens == "" {
		return nil, nil
	}

	items := strings.Fields(apiTokens)
	credentials := make(apiTokenSet, 0, len(items))
	ids := make(map[string]struct{}, len(items))
	digests := make(map[securetoken.Digest]struct{}, len(items))
	for i, item := range items {
		id, token, ok := strings.Cut(item, ":")
		if !ok || id == "" || token == "" {
			return nil, fmt.Errorf("API_TOKENS entry %d must use non-empty id:token format", i+1)
		}
		if _, exists := ids[id]; exists {
			return nil, fmt.Errorf("duplicate API_TOKENS id %q", id)
		}
		digest := securetoken.Hash(token)
		if _, exists := digests[digest]; exists {
			return nil, fmt.Errorf("duplicate token in API_TOKENS entry %d", i+1)
		}
		ids[id] = struct{}{}
		digests[digest] = struct{}{}
		credentials = append(credentials, apiTokenCredential{clientID: id, digest: digest})
	}
	return credentials, nil
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
		if clientID, ok := tokens.match(strings.TrimPrefix(authHeader, "Bearer ")); ok {
			return clientID, true
		}
	}
	if clientID, ok := tokens.match(r.Header.Get("x-api-key")); ok {
		return clientID, true
	}
	return "", false
}

func (tokens apiTokenSet) match(candidate string) (string, bool) {
	candidateDigest := securetoken.Hash(candidate)
	clientID := ""
	matched := false
	// Scan every digest so match position does not affect comparison work.
	for _, credential := range tokens {
		if securetoken.Equal(candidateDigest, credential.digest) {
			clientID = credential.clientID
			matched = true
		}
	}
	return clientID, matched
}
