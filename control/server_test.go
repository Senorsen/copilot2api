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
			server := NewServer(nil, test.adminToken, "", nil, nil)
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
