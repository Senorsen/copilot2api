package control

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/whtsky/copilot2api/stats"
)

func TestHandleUsageQueryCombinesClientAndReasoningFilters(t *testing.T) {
	baseDir := t.TempDir()
	recorder := stats.NewRecorder(baseDir)
	timestamp := time.Date(2026, time.February, 1, 12, 0, 0, 0, time.UTC)
	recorder.Record(stats.Entry{Timestamp: timestamp, ClientID: "client-a", AccountID: "acc", Model: "model", ReasoningEffort: "high", TokensTotal: 1})
	recorder.Record(stats.Entry{Timestamp: timestamp, ClientID: "client-b", AccountID: "acc", Model: "model", ReasoningEffort: "high", TokensTotal: 2})
	recorder.Record(stats.Entry{Timestamp: timestamp, ClientID: "client-b", AccountID: "acc", Model: "model", ReasoningEffort: "low", TokensTotal: 4})
	recorder.Close()

	server := &Server{statsDir: baseDir}
	request := httptest.NewRequest(http.MethodGet, "/usage?start=2026-02-01&end=2026-02-01&reasoning_effort=HIGH&client_id=client-b", nil)
	response := httptest.NewRecorder()
	server.handleUsageQuery(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	var got []stats.AggregatedEntry
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(response) = %d, want 1: %#v", len(got), got)
	}
	if got[0].ClientID != "client-b" || got[0].ReasoningEffort != "high" || got[0].TokensTotal != 2 {
		t.Fatalf("response entry = %#v, want client-b high effort with 2 tokens", got[0])
	}
}

func TestHandleUsageQueryUsesRequestedTimezone(t *testing.T) {
	baseDir := t.TempDir()
	recorder := stats.NewRecorder(baseDir)
	for i, timestamp := range []time.Time{
		time.Date(2026, time.January, 31, 15, 59, 59, 0, time.UTC),
		time.Date(2026, time.January, 31, 16, 0, 0, 0, time.UTC),
		time.Date(2026, time.February, 1, 15, 59, 59, 0, time.UTC),
		time.Date(2026, time.February, 1, 16, 0, 0, 0, time.UTC),
	} {
		recorder.Record(stats.Entry{Timestamp: timestamp, AccountID: "acc", Model: "model", TokensTotal: 1 << i})
	}
	recorder.Close()

	server := &Server{statsDir: baseDir}
	tests := []struct {
		name        string
		query       string
		tokensTotal int
	}{
		{name: "requested timezone", query: "start=2026-02-01&end=2026-02-01&timezone=Asia%2FShanghai", tokensTotal: 6},
		{name: "default UTC", query: "start=2026-02-01&end=2026-02-01", tokensTotal: 12},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/usage?"+test.query, nil)
			response := httptest.NewRecorder()
			server.handleUsageQuery(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
			}
			var got []stats.AggregatedEntry
			if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
				t.Fatalf("Decode() error = %v", err)
			}
			want := []stats.AggregatedEntry{{
				Date:            "2026-02-01",
				ClientID:        "default",
				AccountID:       "acc",
				Model:           "model",
				ReasoningEffort: "unspecified",
				TokensTotal:     test.tokensTotal,
				RequestCount:    2,
			}}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("response = %#v, want %#v", got, want)
			}
		})
	}
}

func TestHandleUsageQueryRejectsInvalidTimezone(t *testing.T) {
	server := &Server{statsDir: t.TempDir()}
	request := httptest.NewRequest(http.MethodGet, "/usage?start=2026-02-01&end=2026-02-01&timezone=not-a-zone", nil)
	response := httptest.NewRecorder()
	server.handleUsageQuery(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, http.StatusBadRequest, response.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if !strings.Contains(body["error"], "invalid timezone") {
		t.Fatalf("error = %q, want invalid timezone error", body["error"])
	}
}
