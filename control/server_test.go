package control

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdminAuthMiddleware(t *testing.T) {
	tests := []struct {
		name       string
		adminToken string
		path       string
		header     string
		wantStatus int
		wantCalled bool
	}{
		{name: "valid token", adminToken: "admin-secret", path: "/accounts", header: "Bearer admin-secret", wantStatus: http.StatusNoContent, wantCalled: true},
		{name: "wrong same-length token", adminToken: "admin-secret", path: "/accounts", header: "Bearer wrong-secret", wantStatus: http.StatusUnauthorized},
		{name: "wrong different-length token", adminToken: "admin-secret", path: "/accounts", header: "Bearer wrong", wantStatus: http.StatusUnauthorized},
		{name: "missing token", adminToken: "admin-secret", path: "/accounts", wantStatus: http.StatusUnauthorized},
		{name: "auth disabled", path: "/accounts", wantStatus: http.StatusNoContent, wantCalled: true},
		{name: "dashboard bypass", adminToken: "admin-secret", path: "/dashboard", wantStatus: http.StatusNoContent, wantCalled: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := NewServer(nil, test.adminToken, "", nil, nil, "dev", "")
			called := false
			handler := server.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusNoContent)
			}))
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			if test.header != "" {
				request.Header.Set("Authorization", test.header)
			}
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
			if called != test.wantCalled {
				t.Fatalf("next handler called = %v, want %v", called, test.wantCalled)
			}
		})
	}
}

func TestDashboardShowsEscapedCommit(t *testing.T) {
	server := &Server{commit: `abc12345<script>`}
	request := httptest.NewRequest("GET", "/dashboard", nil)
	response := httptest.NewRecorder()

	server.handleDashboard(response, request)

	body := response.Body.String()
	if !strings.Contains(body, `data-commit="abc12345&lt;script&gt;"`) {
		t.Fatalf("dashboard does not contain escaped commit: %q", body)
	}
	if strings.Contains(body, "{{BUILD_COMMIT}}") {
		t.Fatal("dashboard still contains the build commit placeholder")
	}
}

func TestDashboardRendersBasePath(t *testing.T) {
	for _, test := range []struct{ basePath, want string }{
		{basePath: "", want: "/"},
		{basePath: "/", want: "/"},
		{basePath: "/copilot/", want: "/copilot/"},
		{basePath: "/tools/copilot/", want: "/tools/copilot/"},
		{basePath: "/copilot", want: "/copilot/"},
		{basePath: "/tools/copilot", want: "/tools/copilot/"},
	} {
		t.Run(test.basePath, func(t *testing.T) {
			server := &Server{commit: "dev", basePath: test.basePath}
			response := httptest.NewRecorder()
			server.handleDashboard(response, httptest.NewRequest(http.MethodGet, "/dashboard", nil))

			body := response.Body.String()
			if !strings.Contains(body, `<base href="`+test.want+`">`) ||
				!strings.Contains(body, `src="dashboard/chart.umd.min.js"`) {
				t.Fatalf("dashboard does not use base path %q", test.want)
			}
			if strings.Contains(body, "{{BASE_PATH}}") {
				t.Fatal("dashboard still contains the base path placeholder")
			}
		})
	}
}

func TestDashboardEscapesRawBasePath(t *testing.T) {
	server := &Server{basePath: `/copilot/" onload="alert(1)`}
	response := httptest.NewRecorder()
	server.handleDashboard(response, httptest.NewRequest(http.MethodGet, "/dashboard", nil))
	if strings.Contains(response.Body.String(), `onload="alert(1)`) {
		t.Fatal("dashboard base path introduced an HTML attribute")
	}
}

func TestDashboardBehindStrippingProxy(t *testing.T) {
	server := NewServer(nil, "secret", t.TempDir(), nil, nil, "dev", "/copilot")
	upstream := server.Handler()
	proxy := http.StripPrefix("/copilot", upstream)
	for _, test := range []struct {
		path, token string
		status      int
		contains    string
	}{
		{path: "/copilot/dashboard", status: http.StatusOK, contains: `<base href="/copilot/">`},
		{path: "/copilot/dashboard/chart.umd.min.js", status: http.StatusOK, contains: "Chart.js"},
		{path: "/copilot/usage?start=2026-09-01&end=2026-09-01", status: http.StatusUnauthorized},
		{path: "/copilot/usage?start=2026-09-01&end=2026-09-01", token: "secret", status: http.StatusOK, contains: "[]"},
		{path: "/copilot/usage/pricing", token: "secret", status: http.StatusServiceUnavailable},
	} {
		t.Run(test.path+test.token, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			if test.token != "" {
				request.Header.Set("Authorization", "Bearer "+test.token)
			}
			response := httptest.NewRecorder()
			proxy.ServeHTTP(response, request)
			if response.Code != test.status || (test.contains != "" && !strings.Contains(response.Body.String(), test.contains)) {
				t.Fatalf("GET %s = %d, body %q; want status %d with %q", test.path, response.Code, response.Body.String(), test.status, test.contains)
			}
		})
	}

	response := httptest.NewRecorder()
	upstream.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/dashboard", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("unprefixed backend route = %d, want 200", response.Code)
	}
}

func TestDashboardIncludesBrowserTimezoneQuery(t *testing.T) {
	server := &Server{commit: "dev"}
	request := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	response := httptest.NewRecorder()

	server.handleDashboard(response, request)

	body := response.Body.String()
	if !strings.Contains(body, "Intl.DateTimeFormat().resolvedOptions().timeZone") {
		t.Fatal("dashboard does not detect the browser timezone")
	}
	if !strings.Contains(body, "timezone: clientTimeZone") {
		t.Fatal("dashboard does not include the browser timezone in usage queries")
	}
	if !strings.Contains(body, "return fmtLocalDate(tmp);") {
		t.Fatal("dashboard does not keep week grouping in the browser timezone")
	}
}
