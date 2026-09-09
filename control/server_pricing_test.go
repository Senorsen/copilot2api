package control

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/whtsky/copilot2api/internal/models"
	"github.com/whtsky/copilot2api/internal/upstream"
	"github.com/whtsky/copilot2api/stats"
)

type pricingTestTransport func(*http.Request) (*http.Response, error)

func (f pricingTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type pricingTestTokenProvider struct {
	baseURL string
}

func (p pricingTestTokenProvider) GetToken(context.Context) (string, error) {
	return "test-token", nil
}

func (p pricingTestTokenProvider) GetBaseURL() string { return p.baseURL }

// pricingTestCache loads a small on-disk fixture and intercepts the constructor's
// automatic refresh. Tests using it must not run in parallel: PricingCache uses
// http.DefaultTransport rather than accepting an injected HTTP client.
func pricingTestCache(t *testing.T, fixture string) *stats.PricingCache {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pricing_cache.json"), []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}

	originalTransport := http.DefaultTransport
	refreshStarted := make(chan struct{}, 1)
	http.DefaultTransport = pricingTestTransport(func(r *http.Request) (*http.Response, error) {
		refreshStarted <- struct{}{}
		// Never delegate to a real transport, even if the pricing URL changes.
		// An unsuccessful refresh leaves the disk fixture intact.
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("offline test fixture")),
			Request:    r,
		}, nil
	})
	pc := stats.NewPricingCache(dir)
	t.Cleanup(func() {
		pc.Close()
		// Wait until the background fetch has captured our transport before
		// restoring the global; otherwise cleanup could allow a real fetch.
		<-refreshStarted
		http.DefaultTransport = originalTransport
	})
	return pc
}

func TestHandleUsagePricingModelSelection(t *testing.T) {
	// Synthetic rates only; the retired model deliberately exists in pricing
	// but not in the current account catalog, reproducing the historical gap.
	const currentPrice = `{"input_cost_per_token":0.000001,"output_cost_per_token":0.000002}`
	const historicalPrice = `{"input_cost_per_token":0.000005,"output_cost_per_token":0.000025,"cache_read_input_token_cost":0.0000005,"cache_creation_input_token_cost":0.00000625}`
	pc := pricingTestCache(t, `{"gpt-current":`+currentPrice+`,"anthropic/claude-retired":`+historicalPrice+`}`)

	var catalogRequests atomic.Int32
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		catalogRequests.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/models" {
			t.Errorf("unexpected catalog request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"id":"gpt-current"}]}`)
	}))
	t.Cleanup(catalog.Close)
	client := &upstream.Client{
		TokenProvider: pricingTestTokenProvider{baseURL: catalog.URL},
		HTTPClient:    catalog.Client(),
	}
	modelCache := models.NewCache(func() *upstream.Client { return client }, time.Hour)
	unavailableCatalog := models.NewCache(func() *upstream.Client {
		t.Error("explicit models must not consult the unavailable catalog")
		return nil
	}, time.Hour)

	tests := []struct {
		name                string
		query               string
		catalog             *models.Cache
		want                map[string]string
		wantCatalogRequests int32
	}{
		{
			name:  "explicit historical model bypasses cold current catalog",
			query: "models=" + url.QueryEscape("claude-retired"), catalog: modelCache,
			want: map[string]string{"claude-retired": historicalPrice},
		},
		{
			name:  "explicit current and historical models trim blanks and omit unknown prices",
			query: "models=" + url.QueryEscape(" gpt-current , ,\tclaude-retired\n,unknown-model,claude-retired,"), catalog: modelCache,
			want: map[string]string{"gpt-current": currentPrice, "claude-retired": historicalPrice},
		},
		{
			name:  "unknown model is omitted rather than assigned zero pricing",
			query: "models=unknown-model", catalog: modelCache, want: map[string]string{},
		},
		{
			name:  "blank model names do not fall back to current catalog",
			query: "models=" + url.QueryEscape(" , \t, "), catalog: modelCache, want: map[string]string{},
		},
		{
			name:  "historical prices work without a catalog",
			query: "models=claude-retired", want: map[string]string{"claude-retired": historicalPrice},
		},
		{
			name:  "historical prices do not depend on catalog availability",
			query: "models=claude-retired", catalog: unavailableCatalog,
			want: map[string]string{"claude-retired": historicalPrice},
		},
		{
			name: "default uses only currently advertised models", catalog: modelCache,
			want: map[string]string{"gpt-current": currentPrice}, wantCatalogRequests: 1,
		},
		{
			name:  "empty models parameter uses current catalog",
			query: "models=", catalog: modelCache, want: map[string]string{"gpt-current": currentPrice},
		},
		{
			name: "no models and no catalog returns an empty object", want: map[string]string{},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := &Server{pricingCache: pc, modelsCache: test.catalog}
			request := httptest.NewRequest(http.MethodGet, "/usage/pricing?"+test.query, nil)
			response := httptest.NewRecorder()
			before := catalogRequests.Load()
			server.handleUsagePricing(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
			}
			if got := response.Header().Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", got)
			}
			if got := catalogRequests.Load() - before; got != test.wantCatalogRequests {
				t.Errorf("catalog requests = %d, want %d", got, test.wantCatalogRequests)
			}
			var got map[string]map[string]float64
			if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
				t.Fatalf("Decode() error = %v", err)
			}
			want := make(map[string]map[string]float64, len(test.want))
			for model, raw := range test.want {
				var price map[string]float64
				if err := json.Unmarshal([]byte(raw), &price); err != nil {
					t.Fatal(err)
				}
				want[model] = price
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("pricing = %#v, want %#v", got, want)
			}
		})
	}
}

func TestHandleUsagePricingRejectsUnavailableCacheAndWrongMethod(t *testing.T) {
	for _, test := range []struct {
		name   string
		method string
		status int
	}{
		{name: "missing pricing cache", method: http.MethodGet, status: http.StatusServiceUnavailable},
		{name: "wrong method", method: http.MethodPost, status: http.StatusMethodNotAllowed},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := &Server{}
			response := httptest.NewRecorder()
			server.handleUsagePricing(response, httptest.NewRequest(test.method, "/usage/pricing?models=claude-retired", nil))
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, test.status, response.Body.String())
			}
		})
	}
}
