// Package debug provides request body capture for debugging specific models.
// Enable via COPILOT2API_DEBUG_MODELS=gpt-5.4,gpt-5.5 (comma-separated substrings).
// Captured requests are saved as formatted JSON under {dataDir}/debug/{model}/.
package debug

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	debugModels []string
	dataDir     string
	once        sync.Once
)

// Init loads the debug model list from env and sets the base data directory.
// Call once at startup. dataDir should be the persistent config directory
// (e.g. /root/.config/copilot2api).
func Init(baseDir string) {
	once.Do(func() {
		dataDir = baseDir
		raw := os.Getenv("COPILOT2API_DEBUG_MODELS")
		if raw == "" {
			return
		}
		for _, m := range strings.Split(raw, ",") {
			m = strings.TrimSpace(m)
			if m != "" {
				debugModels = append(debugModels, m)
			}
		}
		if len(debugModels) > 0 {
			slog.Info("debug capture enabled", "models", debugModels, "dir", filepath.Join(dataDir, "debug"))
		}
	})
}

// CaptureRequest saves the request body as formatted JSON if the model matches
// any configured debug model. Safe to call even when debug is disabled (no-op).
func CaptureRequest(model string, body []byte) {
	if len(debugModels) == 0 || len(body) == 0 {
		return
	}

	matched := ""
	for _, m := range debugModels {
		if strings.Contains(model, m) {
			matched = m
			break
		}
	}
	if matched == "" {
		return
	}

	// Format JSON with indent
	var pretty json.RawMessage
	if err := json.Unmarshal(body, &pretty); err != nil {
		slog.Warn("debug capture: invalid JSON body", "model", model, "error", err)
		return
	}
	formatted, err := json.MarshalIndent(pretty, "", "  ")
	if err != nil {
		slog.Warn("debug capture: failed to format JSON", "model", model, "error", err)
		return
	}

	// Build path: {dataDir}/debug/{matched}/{datetime}-{rand4}.json
	dir := filepath.Join(dataDir, "debug", matched)
	if err := os.MkdirAll(dir, 0755); err != nil {
		slog.Warn("debug capture: failed to create dir", "dir", dir, "error", err)
		return
	}

	randBytes := make([]byte, 2)
	rand.Read(randBytes)
	filename := fmt.Sprintf("%s-%s.json", time.Now().Format("20060102-150405"), hex.EncodeToString(randBytes))
	path := filepath.Join(dir, filename)

	if err := os.WriteFile(path, formatted, 0644); err != nil {
		slog.Warn("debug capture: failed to write file", "path", path, "error", err)
		return
	}

	slog.Debug("debug capture: saved request", "model", model, "path", path, "size", len(formatted))
}
