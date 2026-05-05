package auth

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// AccountManager manages multiple auth accounts with ID-based lookup.
// It replaces the old round-robin MultiClient.
type AccountManager struct {
	mu       sync.RWMutex
	accounts map[string]*Client // account_id -> client
	baseDir  string
	// defaultID is the account used when no account is specified in path
	defaultID string
}

// NewAccountManager creates an account manager from a base directory.
// Each subdirectory name is the account_id.
// If no subdirectories exist but credentials.json is present, creates a "default" account.
func NewAccountManager(baseDir string) (*AccountManager, error) {
	if err := os.MkdirAll(baseDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create base directory: %w", err)
	}

	am := &AccountManager{
		accounts: make(map[string]*Client),
		baseDir:  baseDir,
	}

	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read base directory: %w", err)
	}

	var firstID string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id := entry.Name()
		dir := filepath.Join(baseDir, id)
		client, err := NewClient(dir)
		if err != nil {
			slog.Warn("skipping account directory", "id", id, "error", err)
			continue
		}
		am.accounts[id] = client
		if firstID == "" {
			firstID = id
		}
	}

	// Backward compat: if no subdirs but credentials.json exists at baseDir
	if len(am.accounts) == 0 {
		credPath := filepath.Join(baseDir, "credentials.json")
		if _, err := os.Stat(credPath); err == nil {
			client, err := NewClient(baseDir)
			if err != nil {
				return nil, fmt.Errorf("failed to initialize default account: %w", err)
			}
			am.accounts["default"] = client
			firstID = "default"
		}
	}

	am.defaultID = firstID
	return am, nil
}

// GetClient returns the client for a given account ID.
func (am *AccountManager) GetClient(accountID string) (*Client, bool) {
	am.mu.RLock()
	defer am.mu.RUnlock()
	c, ok := am.accounts[accountID]
	return c, ok
}

// GetDefaultClient returns the default account client.
func (am *AccountManager) GetDefaultClient() (*Client, bool) {
	am.mu.RLock()
	defer am.mu.RUnlock()
	if am.defaultID == "" {
		return nil, false
	}
	c, ok := am.accounts[am.defaultID]
	return c, ok
}

// AddAccount adds an authenticated account.
func (am *AccountManager) AddAccount(id string, client *Client) {
	am.mu.Lock()
	defer am.mu.Unlock()
	am.accounts[id] = client
	if am.defaultID == "" {
		am.defaultID = id
	}
}

// RemoveAccount removes an account and deletes its storage directory.
func (am *AccountManager) RemoveAccount(id string) error {
	am.mu.Lock()
	defer am.mu.Unlock()
	if _, ok := am.accounts[id]; !ok {
		return fmt.Errorf("account %s not found", id)
	}
	delete(am.accounts, id)
	if am.defaultID == id {
		am.defaultID = ""
		for k := range am.accounts {
			am.defaultID = k
			break
		}
	}
	// Remove storage directory
	dir := filepath.Join(am.baseDir, id)
	return os.RemoveAll(dir)
}

// ListAccounts returns all account IDs and their GitHub usernames.
func (am *AccountManager) ListAccounts() []AccountInfo {
	am.mu.RLock()
	defer am.mu.RUnlock()
	var result []AccountInfo
	for id, client := range am.accounts {
		info := AccountInfo{
			ID:        id,
			IsDefault: id == am.defaultID,
		}
		client.mu.RLock()
		if client.creds.GitHubToken != "" {
			info.HasToken = true
		}
		if client.creds.GitHubUsername != "" {
			info.GitHubUsername = client.creds.GitHubUsername
		}
		if client.creds.CopilotToken != nil {
			info.TokenValid = client.creds.CopilotToken.IsTokenUsable()
		}
		client.mu.RUnlock()
		result = append(result, info)
	}
	return result
}

// AccountInfo is returned by the list endpoint.
type AccountInfo struct {
	ID             string `json:"id"`
	GitHubUsername string `json:"github_username,omitempty"`
	HasToken       bool   `json:"has_token"`
	TokenValid     bool   `json:"token_valid"`
	IsDefault      bool   `json:"is_default"`
}

// EnsureAllAuthenticated authenticates all existing accounts at startup.
func (am *AccountManager) EnsureAllAuthenticated(ctx context.Context) error {
	am.mu.RLock()
	defer am.mu.RUnlock()
	for id, client := range am.accounts {
		slog.Info("authenticating account", "id", id)
		if err := client.EnsureAuthenticated(ctx); err != nil {
			return fmt.Errorf("account %s authentication failed: %w", id, err)
		}
	}
	return nil
}

// Count returns number of accounts.
func (am *AccountManager) Count() int {
	am.mu.RLock()
	defer am.mu.RUnlock()
	return len(am.accounts)
}

// BaseDir returns the base directory for account storage.
func (am *AccountManager) BaseDir() string {
	return am.baseDir
}

// --- TokenProvider implementation for a specific account ---

// AccountTokenProvider wraps a single Client as an upstream.TokenProvider.
type AccountTokenProvider struct {
	client *Client
}

func NewAccountTokenProvider(client *Client) *AccountTokenProvider {
	return &AccountTokenProvider{client: client}
}

func (p *AccountTokenProvider) GetToken(ctx context.Context) (string, error) {
	return p.client.GetToken(ctx)
}

func (p *AccountTokenProvider) GetBaseURL() string {
	return p.client.GetBaseURL()
}

// --- Default/fallback TokenProvider that uses AM's default account ---

// DefaultTokenProvider uses the AccountManager's default account.
// Satisfies upstream.TokenProvider.
type DefaultTokenProvider struct {
	AM *AccountManager
}

func (d *DefaultTokenProvider) GetToken(ctx context.Context) (string, error) {
	client, ok := d.AM.GetDefaultClient()
	if !ok {
		return "", fmt.Errorf("no default account configured")
	}
	return client.GetToken(ctx)
}

func (d *DefaultTokenProvider) GetBaseURL() string {
	client, ok := d.AM.GetDefaultClient()
	if !ok {
		return DefaultBaseURL
	}
	return client.GetBaseURL()
}
