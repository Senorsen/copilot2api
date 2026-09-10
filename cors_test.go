package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCORSConfig(t *testing.T) {
	c, e := parseCORSConfig("https://one.example,https://two.example https://one.example", "claude-office,claude-office", "x-custom", "")
	if e != nil || len(c.origins) != 3 || !c.headers["authorization"] || !c.headers["x-custom"] {
		t.Fatalf("config %#v %v", c, e)
	}
	for _, args := range [][4]string{{"https://ok.example/path", "", "", ""}, {"https://u:p@ok.example", "", "", ""}, {"null", "", "", ""}, {"", "unknown", "", ""}, {"*", "", "", "true"}, {"https://ok.example", "", "x-evil: bad", ""}, {"https://ok.example?q=x", "", "", ""}} {
		if _, e := parseCORSConfig(args[0], args[1], args[2], args[3]); e == nil {
			t.Fatalf("accepted %q", args)
		}
	}
	// No small fixed limit on user-specified origins.
	var origins []string
	for i := 0; i < 200; i++ {
		origins = append(origins, fmt.Sprintf("https://app%d.example", i))
	}
	c, e = parseCORSConfig(strings.Join(origins, ","), "", "", "")
	if e != nil || len(c.origins) != 200 {
		t.Fatalf("multiple origins failed: %v", e)
	}
}
func TestCORSPreflightBeforeAuthAndAllErrors(t *testing.T) {
	c, e := parseCORSConfig("https://custom.example", "claude-office", "", "")
	if e != nil {
		t.Fatal(e)
	}
	tokens, e := parseAPITokenConfig("test-token", "")
	if e != nil {
		t.Fatal(e)
	}
	next := apiTokenAuth(tokens, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/error":
			http.Error(w, "upstream unavailable", 502)
		case "/stream":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: ok\n\n"))
			w.(http.Flusher).Flush()
		default:
			_, _ = w.Write([]byte("ok"))
		}
	}))
	h := c.wrap(next)
	cases := []struct {
		method, origin, path, token, headers, requestMethod string
		code                                                int
		allow                                               bool
	}{
		{"OPTIONS", "https://pivot.claude.ai", "/v1/messages", "", "x-api-key, Authorization, anthropic-version, content-type", "POST", 204, true},
		{"OPTIONS", "https://custom.example", "/v1/models", "", "x-api-key", "GET", 204, true},
		{"GET", "https://pivot.claude.ai", "/v1/models", "", "", "", 401, true},
		{"POST", "https://pivot.claude.ai", "/v1/messages", "wrong", "", "", 401, true},
		{"POST", "https://pivot.claude.ai", "/error", "test-token", "", "", 502, true},
		{"POST", "https://custom.example", "/stream", "test-token", "", "", 200, true},
		{"OPTIONS", "https://evil.example", "/v1/messages", "", "x-api-key", "POST", 403, false},
		{"OPTIONS", "https://pivot.claude.ai.evil.example", "/v1/messages", "", "x-api-key", "POST", 403, false},
		{"OPTIONS", "https://pivot.claude.ai", "/v1/messages", "", "x-not-allowed", "POST", 403, true},
		{"OPTIONS", "https://pivot.claude.ai", "/v1/messages", "", "x-api-key", "DELETE", 403, true},
		{"GET", "", "/v1/models", "test-token", "", "", 200, false},
		{"OPTIONS", "", "/v1/messages", "", "", "", 401, false},
	}
	for _, tt := range cases {
		t.Run(tt.method+tt.origin+tt.path+tt.headers+tt.requestMethod, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, tt.path, nil)
			r.Header.Set("Origin", tt.origin)
			r.Header.Set("x-api-key", tt.token)
			r.Header.Set("Access-Control-Request-Headers", tt.headers)
			r.Header.Set("Access-Control-Request-Method", tt.requestMethod)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tt.code {
				t.Fatalf("code %d body %s", w.Code, w.Body)
			}
			allow := w.Header().Get("Access-Control-Allow-Origin")
			if tt.allow && allow != tt.origin || !tt.allow && allow != "" {
				t.Fatalf("allow %q", allow)
			}
			if strings.Count(allow, tt.origin) > 1 && tt.origin != "" {
				t.Fatal("duplicate origin")
			}
			if w.Code == 204 && !strings.Contains(w.Header().Get("Access-Control-Allow-Headers"), "authorization") {
				t.Fatal("authorization must be explicit")
			}
			if tt.path == "/stream" && !w.Flushed {
				t.Fatal("SSE flusher lost")
			}
		})
	}
}
func TestCORSDisabledAndWildcard(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) })
	for _, origin := range []string{"", "*"} {
		c, e := parseCORSConfig(origin, "", "", "")
		if e != nil {
			t.Fatal(e)
		}
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Origin", "https://other.example")
		w := httptest.NewRecorder()
		c.wrap(next).ServeHTTP(w, r)
		if w.Code != 401 || w.Header().Get("Access-Control-Allow-Origin") != origin {
			t.Fatalf("disabled/wildcard mismatch")
		}
	}
}

func TestOfficeSDKStreamHelperPreflight(t *testing.T) {
	c, e := parseCORSConfig("", "claude-office", "", "")
	if e != nil {
		t.Fatal(e)
	}
	h := c.wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("preflight reached authentication/upstream") }))
	for _, helper := range []string{"X-Stainless-Helper-Method", "x-stainless-helper", "X-Stainless-Helper-Method, x-stainless-helper"} {
		r := httptest.NewRequest("OPTIONS", "/api/account/v1/messages", nil)
		r.Header.Set("Origin", "https://pivot.claude.ai")
		r.Header.Set("Access-Control-Request-Method", "POST")
		r.Header.Set("Access-Control-Request-Headers", "authorization,content-type,anthropic-version,anthropic-dangerous-direct-browser-access,x-stainless-lang,x-stainless-package-version,x-stainless-runtime,x-stainless-runtime-version,x-stainless-os,x-stainless-arch,x-stainless-retry-count,x-stainless-timeout,"+helper)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 204 {
			t.Fatalf("SDK stream helper preflight: %d %s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Header().Get("Access-Control-Allow-Headers"), "x-stainless-helper-method") {
			t.Fatal("stream helper not explicitly allowed")
		}
	}
}
func TestCORSDiagnosticHeaderNameIsBounded(t *testing.T) {
	if corsDiagnosticHeaderName("x-custom") != "x-custom" {
		t.Fatal("valid name missing")
	}
	for _, name := range []string{"", strings.Repeat("a", 81), "authorization: Bearer secret", "evil\nlog", "x_"} {
		if corsDiagnosticHeaderName(name) != "(invalid)" {
			t.Fatal("unsafe diagnostic name accepted")
		}
	}
}
