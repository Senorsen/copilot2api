package main

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/whtsky/copilot2api/internal/reqctx"
)

func TestParseAPITokenConfig(t *testing.T) {
	tests := []struct {
		name      string
		apiToken  string
		apiTokens string
		want      apiTokenSet
		wantErr   bool
	}{
		{
			name:     "single token",
			apiToken: "single-secret",
			want:     apiTokenSet{"single-secret": reqctx.DefaultClientID},
		},
		{
			name:      "multiple tokens with punctuation",
			apiTokens: "alice:token:with:colons bob:token,with,commas",
			want: apiTokenSet{
				"token:with:colons": "alice",
				"token,with,commas": "bob",
			},
		},
		{name: "empty configuration"},
		{name: "whitespace-only API_TOKENS", apiTokens: " \t\n"},
		{name: "mutually exclusive", apiToken: "one", apiTokens: "alice:two", wantErr: true},
		{name: "missing separator", apiTokens: "alice", wantErr: true},
		{name: "empty id", apiTokens: ":token", wantErr: true},
		{name: "empty token", apiTokens: "alice:", wantErr: true},
		{name: "duplicate id", apiTokens: "alice:one alice:two", wantErr: true},
		{name: "duplicate token", apiTokens: "alice:same bob:same", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAPITokenConfig(tt.apiToken, tt.apiTokens)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseAPITokenConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseAPITokenConfig() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestAPITokenAuth(t *testing.T) {
	tokens := apiTokenSet{
		"secret:with,commas": "alice",
		"second-secret":      "bob",
	}
	tests := []struct {
		name       string
		headers    map[string]string
		wantStatus int
		wantID     string
	}{
		{
			name:       "bearer token",
			headers:    map[string]string{"Authorization": "Bearer second-secret"},
			wantStatus: http.StatusNoContent,
			wantID:     "bob",
		},
		{
			name:       "x-api-key with punctuation",
			headers:    map[string]string{"x-api-key": "secret:with,commas"},
			wantStatus: http.StatusNoContent,
			wantID:     "alice",
		},
		{
			name: "valid x-api-key despite invalid bearer",
			headers: map[string]string{
				"Authorization": "Bearer invalid",
				"x-api-key":     "secret:with,commas",
			},
			wantStatus: http.StatusNoContent,
			wantID:     "alice",
		},
		{
			name:       "unknown token",
			headers:    map[string]string{"Authorization": "Bearer invalid"},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "missing token",
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			gotClientID := ""
			handler := apiTokenAuth(tokens, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				gotClientID = reqctx.GetClientID(r.Context())
				w.WriteHeader(http.StatusNoContent)
			}))

			request := httptest.NewRequest(http.MethodGet, "/", nil)
			for name, value := range tt.headers {
				request.Header.Set(name, value)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, tt.wantStatus)
			}
			if tt.wantID == "" {
				if called {
					t.Fatal("next handler was called for rejected request")
				}
				if got := response.Header().Get("Content-Type"); got != "application/json" {
					t.Fatalf("Content-Type = %q, want application/json", got)
				}
				return
			}
			if !called {
				t.Fatal("next handler was not called for authenticated request")
			}
			if gotClientID != tt.wantID {
				t.Fatalf("client ID = %q, want %q", gotClientID, tt.wantID)
			}
		})
	}
}
