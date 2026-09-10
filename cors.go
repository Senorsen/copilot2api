package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Presets can be combined with arbitrary explicit origins; disabled by default.
var corsPresets = map[string][]string{"claude-office": {"https://pivot.claude.ai"}}

const corsDefaultHeaders = "authorization,x-api-key,content-type,anthropic-version,anthropic-beta,anthropic-dangerous-direct-browser-access,x-stainless-lang,x-stainless-package-version,x-stainless-os,x-stainless-arch,x-stainless-runtime,x-stainless-runtime-version,x-stainless-retry-count,x-stainless-timeout,x-stainless-helper-method,x-stainless-helper"

type corsConfig struct {
	origins     map[string]bool
	headers     map[string]bool
	headerList  string
	credentials bool
}

func corsList(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' || r == ' ' || r == '\t' })
}
func parseCORSConfig(origins, presets, extraHeaders, credentials string) (corsConfig, error) {
	c := corsConfig{origins: map[string]bool{}, headers: map[string]bool{}}
	if credentials != "" {
		v, e := strconv.ParseBool(credentials)
		if e != nil {
			return c, fmt.Errorf("CORS credentials must be a boolean")
		}
		c.credentials = v
	}
	entries := corsList(origins)
	for _, p := range corsList(presets) {
		values, ok := corsPresets[p]
		if !ok {
			return c, fmt.Errorf("unknown CORS preset %q", p)
		}
		entries = append(entries, values...)
	}
	for _, origin := range entries {
		if origin == "*" {
			c.origins[origin] = true
			continue
		}
		u, e := url.Parse(origin)
		if e != nil || u == nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || u.Opaque != "" || strings.ContainsAny(origin, "\r\n\x00") {
			return c, fmt.Errorf("CORS origin must be an exact http(s) origin without path, userinfo, query or fragment")
		}
		c.origins[origin] = true
	}
	if c.origins["*"] && c.credentials {
		return c, fmt.Errorf("CORS wildcard cannot be combined with credentials")
	}
	for _, h := range corsList(corsDefaultHeaders + "," + extraHeaders) {
		h = strings.ToLower(h)
		for _, r := range h {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return c, fmt.Errorf("invalid CORS allowed header")
			}
		}
		c.headers[h] = true
	}
	list := make([]string, 0, len(c.headers))
	for h := range c.headers {
		list = append(list, h)
	}
	sort.Strings(list)
	c.headerList = strings.Join(list, ", ")
	return c, nil
}
func (c corsConfig) wrap(next http.Handler) http.Handler {
	if len(c.origins) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No ResponseWriter wrapper: SSE flushing and cancellation pass through.
		w.Header().Add("Vary", "Origin")
		origin := r.Header.Get("Origin")
		allowed := origin != "" && (c.origins[origin] || c.origins["*"])
		preflight := r.Method == http.MethodOptions && origin != "" && r.Header.Get("Access-Control-Request-Method") != ""
		if preflight {
			w.Header().Add("Vary", "Access-Control-Request-Method")
			w.Header().Add("Vary", "Access-Control-Request-Headers")
		}
		if allowed {
			value := origin
			if c.origins["*"] {
				value = "*"
			}
			w.Header().Set("Access-Control-Allow-Origin", value)
			if c.credentials {
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}
			w.Header().Set("Access-Control-Expose-Headers", "Request-Id, X-Request-Id, Retry-After, X-Upstream-Model")
		}
		if preflight {
			method := r.Header.Get("Access-Control-Request-Method")
			if !allowed || (method != "GET" && method != "POST") {
				slog.Warn("CORS preflight rejected", "reason", "origin_or_method")
				http.Error(w, "CORS preflight not allowed", http.StatusForbidden)
				return
			}
			for _, h := range strings.Split(r.Header.Get("Access-Control-Request-Headers"), ",") {
				h = strings.ToLower(strings.TrimSpace(h))
				if h != "" && !c.headers[h] {
					slog.Warn("CORS preflight rejected", "reason", "request_header", "header_name", corsDiagnosticHeaderName(h))
					http.Error(w, "CORS request header not allowed", http.StatusForbidden)
					return
				}
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", c.headerList)
			w.Header().Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Log only a bounded valid header NAME supplied by the browser preflight,
// never credentials, actual header values, URL queries, or request bodies.
func corsDiagnosticHeaderName(name string) string {
	if len(name) == 0 || len(name) > 80 {
		return "(invalid)"
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return "(invalid)"
		}
	}
	return name
}
