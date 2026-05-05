package auth

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
)

// MultiClient manages multiple auth clients and provides round-robin token selection.
// It implements upstream.TokenProvider.
type MultiClient struct {
	clients []*Client
	counter atomic.Uint64
}

// NewMultiClient creates a multi-account client from a list of token directories.
// Each directory should contain (or will create) its own credentials.json.
func NewMultiClient(tokenDirs []string) (*MultiClient, error) {
	if len(tokenDirs) == 0 {
		return nil, fmt.Errorf("no token directories provided")
	}

	clients := make([]*Client, 0, len(tokenDirs))
	for _, dir := range tokenDirs {
		client, err := NewClient(dir)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize client for %s: %w", dir, err)
		}
		clients = append(clients, client)
	}

	return &MultiClient{clients: clients}, nil
}

// NewMultiClientFromBaseDir creates a multi-account client by scanning subdirectories
// of baseDir. Each subdirectory is treated as a separate account's token directory.
// If baseDir has no subdirectories but contains credentials.json directly, it falls
// back to single-account mode (backward compatible).
func NewMultiClientFromBaseDir(baseDir string) (*MultiClient, error) {
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read base directory %s: %w", baseDir, err)
	}

	var subdirs []string
	for _, entry := range entries {
		if entry.IsDir() {
			subdirs = append(subdirs, filepath.Join(baseDir, entry.Name()))
		}
	}

	// If no subdirectories found, use baseDir itself (single-account backward compat)
	if len(subdirs) == 0 {
		return NewMultiClient([]string{baseDir})
	}

	return NewMultiClient(subdirs)
}

// EnsureAuthenticated runs device flow for all accounts that need it.
func (mc *MultiClient) EnsureAuthenticated(ctx context.Context) error {
	for i, client := range mc.clients {
		slog.Info("authenticating account", "index", i, "total", len(mc.clients))
		if err := client.EnsureAuthenticated(ctx); err != nil {
			return fmt.Errorf("account %d authentication failed: %w", i, err)
		}
	}
	return nil
}

// GetToken returns a valid token using round-robin selection across accounts.
func (mc *MultiClient) GetToken(ctx context.Context) (string, error) {
	n := uint64(len(mc.clients))
	start := mc.counter.Add(1) - 1
	
	// Try round-robin starting from current counter, fall through on error
	for i := uint64(0); i < n; i++ {
		idx := (start + i) % n
		token, err := mc.clients[idx].GetToken(ctx)
		if err == nil {
			return token, nil
		}
		slog.Warn("account token unavailable, trying next", "index", idx, "error", err)
	}
	return "", fmt.Errorf("all %d accounts failed to provide a valid token", n)
}

// GetBaseURL returns the base URL from the current round-robin account.
func (mc *MultiClient) GetBaseURL() string {
	n := uint64(len(mc.clients))
	idx := (mc.counter.Load()) % n
	return mc.clients[idx].GetBaseURL()
}

// GetUsageInfo returns usage info from all accounts (satisfies proxy.UsageProvider).
func (mc *MultiClient) GetUsageInfo(ctx context.Context) (interface{}, error) {
	var results []*UsageInfo
	for _, client := range mc.clients {
		info, err := client.GetUsageInfo(ctx)
		if err != nil {
			slog.Warn("failed to get usage info for account", "error", err)
			continue
		}
		results = append(results, info)
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("failed to get usage info from any account")
	}
	if len(results) == 1 {
		return results[0], nil
	}
	return results, nil
}

// Count returns the number of accounts.
func (mc *MultiClient) Count() int {
	return len(mc.clients)
}
