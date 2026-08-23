package stats

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestQueryAggregatesAndFiltersReasoningEffort(t *testing.T) {
	baseDir := t.TempDir()
	dayOne := time.Date(2026, time.February, 1, 12, 0, 0, 0, time.UTC)
	dayTwo := dayOne.AddDate(0, 0, 1)

	entries := []Entry{
		{Timestamp: dayTwo, ClientID: "client-a", AccountID: "acc-a", Username: "alice", Model: "model-a", ReasoningEffort: "minimal", TokensIn: 11, TokensTotal: 11},
		{Timestamp: dayOne, ClientID: "client-b", AccountID: "acc-b", Username: "bob", Model: "model-z", ReasoningEffort: "HIGH", TokensIn: 1, TokensOut: 2, TokensTotal: 3},
		{Timestamp: dayOne, ClientID: "client-a", AccountID: "acc-a", Username: "alice", Model: "model-z", ReasoningEffort: "low", TokensIn: 2, TokensOut: 2, TokensTotal: 4},
		{Timestamp: dayOne, ClientID: "client-a", AccountID: "acc-a", Username: "alice", Model: "model-a", ReasoningEffort: " XHIGH ", TokensIn: 3, TokensOut: 1, TokensCached: 1, TokensTotal: 4},
		{Timestamp: dayOne, ClientID: "client-a", AccountID: "acc-a", Username: "alice", Model: "model-a", ReasoningEffort: "xhigh", TokensIn: 5, TokensOut: 2, TokensCached: 2, TokensNewCache: 1, TokensTotal: 7},
	}
	for _, entry := range entries {
		appendStatsJSON(t, baseDir, entry, entry)
	}

	historical := map[string]any{
		"timestamp":    dayOne,
		"account_id":   "acc-a",
		"username":     "alice",
		"model":        "model-a",
		"tokens_in":    7,
		"tokens_total": 7,
	}
	appendStatsJSON(t, baseDir, entries[3], historical)

	start := time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, time.February, 3, 0, 0, 0, 0, time.UTC)
	got, err := Query(baseDir, "", "", start, end)
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}

	want := []AggregatedEntry{
		{Date: "2026-02-01", ClientID: "default", AccountID: "acc-a", Username: "alice", Model: "model-a", ReasoningEffort: "unspecified", TokensIn: 7, TokensTotal: 7, RequestCount: 1},
		{Date: "2026-02-01", ClientID: "client-a", AccountID: "acc-a", Username: "alice", Model: "model-a", ReasoningEffort: "xhigh", TokensIn: 8, TokensOut: 3, TokensCached: 3, TokensNewCache: 1, TokensTotal: 11, RequestCount: 2},
		{Date: "2026-02-01", ClientID: "client-a", AccountID: "acc-a", Username: "alice", Model: "model-z", ReasoningEffort: "low", TokensIn: 2, TokensOut: 2, TokensTotal: 4, RequestCount: 1},
		{Date: "2026-02-01", ClientID: "client-b", AccountID: "acc-b", Username: "bob", Model: "model-z", ReasoningEffort: "high", TokensIn: 1, TokensOut: 2, TokensTotal: 3, RequestCount: 1},
		{Date: "2026-02-02", ClientID: "client-a", AccountID: "acc-a", Username: "alice", Model: "model-a", ReasoningEffort: "minimal", TokensIn: 11, TokensTotal: 11, RequestCount: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Query() = %#v, want %#v", got, want)
	}

	filtered, err := QueryWithReasoningEffort(baseDir, "", "", " XHIGH ", start, end)
	if err != nil {
		t.Fatalf("QueryWithReasoningEffort() error = %v", err)
	}
	if !reflect.DeepEqual(filtered, want[1:2]) {
		t.Fatalf("QueryWithReasoningEffort(xhigh) = %#v, want %#v", filtered, want[1:2])
	}

	historicalOnly, err := QueryWithReasoningEffort(baseDir, "", "", "unspecified", start, end)
	if err != nil {
		t.Fatalf("QueryWithReasoningEffort(unspecified) error = %v", err)
	}
	if !reflect.DeepEqual(historicalOnly, want[:1]) {
		t.Fatalf("QueryWithReasoningEffort(unspecified) = %#v, want %#v", historicalOnly, want[:1])
	}

	clientOnly, err := QueryWithFilters(baseDir, QueryFilters{ClientID: "client-b"}, start, end)
	if err != nil {
		t.Fatalf("QueryWithFilters(client-b) error = %v", err)
	}
	if !reflect.DeepEqual(clientOnly, want[3:4]) {
		t.Fatalf("QueryWithFilters(client-b) = %#v, want %#v", clientOnly, want[3:4])
	}
}

func TestQueryKeepsClientsSeparate(t *testing.T) {
	baseDir := t.TempDir()
	day := time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)
	timestamp := day.Add(12 * time.Hour)
	for _, entry := range []Entry{
		{Timestamp: timestamp, ClientID: "client-a", AccountID: "acc", Model: "model", TokensTotal: 1},
		{Timestamp: timestamp, ClientID: "client-b", AccountID: "acc", Model: "model", TokensTotal: 2},
	} {
		appendStatsJSON(t, baseDir, entry, entry)
	}

	got, err := Query(baseDir, "", "", day, day.AddDate(0, 0, 1))
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(Query()) = %d, want 2: %#v", len(got), got)
	}
	totals := make(map[string]int, len(got))
	for _, entry := range got {
		if entry.RequestCount != 1 {
			t.Fatalf("request count for %q = %d, want 1", entry.ClientID, entry.RequestCount)
		}
		if _, exists := totals[entry.ClientID]; exists {
			t.Fatalf("duplicate aggregate for client %q: %#v", entry.ClientID, got)
		}
		totals[entry.ClientID] = entry.TokensTotal
	}
	want := map[string]int{"client-a": 1, "client-b": 2}
	if !reflect.DeepEqual(totals, want) {
		t.Fatalf("client totals = %#v, want %#v", totals, want)
	}
}

func TestQueryBucketsUTCRecordsByClientCalendarDay(t *testing.T) {
	tests := []struct {
		name       string
		timezone   string
		date       string
		timestamps []time.Time
	}{
		{
			name:     "crosses UTC date boundary",
			timezone: "Asia/Shanghai",
			date:     "2026-02-01",
			timestamps: []time.Time{
				time.Date(2026, time.January, 31, 15, 59, 0, 0, time.UTC),
				time.Date(2026, time.January, 31, 16, 0, 0, 0, time.UTC),
				time.Date(2026, time.February, 1, 15, 59, 0, 0, time.UTC),
				time.Date(2026, time.February, 1, 16, 0, 0, 0, time.UTC),
			},
		},
		{
			name:     "uses 23-hour DST day",
			timezone: "America/New_York",
			date:     "2026-03-08",
			timestamps: []time.Time{
				time.Date(2026, time.March, 8, 4, 59, 59, 0, time.UTC),
				time.Date(2026, time.March, 8, 5, 0, 0, 0, time.UTC),
				time.Date(2026, time.March, 9, 3, 59, 59, 0, time.UTC),
				time.Date(2026, time.March, 9, 4, 0, 0, 0, time.UTC),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			location, err := time.LoadLocation(test.timezone)
			if err != nil {
				t.Fatalf("LoadLocation() error = %v", err)
			}
			baseDir := t.TempDir()
			recorder := NewRecorder(baseDir)
			for i, timestamp := range test.timestamps {
				recorder.Record(Entry{Timestamp: timestamp, AccountID: "acc", Model: "model", TokensTotal: 1 << i})
			}
			recorder.Close()

			start, err := time.ParseInLocation("2006-01-02", test.date, location)
			if err != nil {
				t.Fatalf("ParseInLocation() error = %v", err)
			}
			got, err := QueryWithFilters(baseDir, QueryFilters{}, start, start.AddDate(0, 0, 1))
			if err != nil {
				t.Fatalf("QueryWithFilters() error = %v", err)
			}
			want := []AggregatedEntry{{
				Date:            test.date,
				ClientID:        "default",
				AccountID:       "acc",
				Model:           "model",
				ReasoningEffort: "unspecified",
				TokensTotal:     6,
				RequestCount:    2,
			}}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("QueryWithFilters() = %#v, want %#v", got, want)
			}
		})
	}
}

func TestRecorderWritesUnspecifiedReasoningEffort(t *testing.T) {
	baseDir := t.TempDir()
	timestamp := time.Date(2026, time.February, 1, 12, 0, 0, 0, time.UTC)
	recorder := NewRecorder(baseDir)
	recorder.Record(Entry{Timestamp: timestamp, AccountID: "acc", Model: "model"})
	recorder.Close()

	path := filepath.Join(baseDir, "acc", "2026", "2026-02-01_model.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var entry Entry
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if entry.ReasoningEffort != "unspecified" {
		t.Fatalf("ReasoningEffort = %q, want unspecified", entry.ReasoningEffort)
	}
	if entry.ClientID != "default" {
		t.Fatalf("ClientID = %q, want default", entry.ClientID)
	}
}

func TestRecorderStoresTimestampsAndPartitionsInUTC(t *testing.T) {
	baseDir := t.TempDir()
	localTimestamp := time.Date(2026, time.February, 1, 0, 30, 0, 0, time.FixedZone("UTC+8", 8*60*60))
	recorder := NewRecorder(baseDir)
	recorder.Record(Entry{Timestamp: localTimestamp, AccountID: "acc", Model: "model"})
	recorder.Close()

	path := filepath.Join(baseDir, "acc", "2026", "2026-01-31_model.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(data), `"timestamp":"2026-01-31T16:30:00Z"`) {
		t.Fatalf("stored record does not contain canonical UTC timestamp: %s", data)
	}
}

func appendStatsJSON(t *testing.T, baseDir string, pathEntry Entry, value any) {
	t.Helper()
	dir := filepath.Join(baseDir, pathEntry.AccountID, pathEntry.Timestamp.Format("2006"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	path := filepath.Join(dir, pathEntry.Timestamp.Format("2006-01-02")+"_"+sanitizeModel(pathEntry.Model)+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("OpenFile() error = %v", err)
	}
	if err := json.NewEncoder(f).Encode(value); err != nil {
		f.Close()
		t.Fatalf("Encode() error = %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}
