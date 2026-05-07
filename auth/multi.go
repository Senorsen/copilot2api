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
type AccountManager struct {
	mu       sync.RWMutex
	accounts map[string]*Client // account_id -> client
	baseDir  string
}

// NewAccountManager creates an account manager from a base directory.
// Each subdirectory name is the account_id.
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

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id := entry.Name()
		dir := filepath.Join(baseDir, id)
		// Skip directories that don't contain credentials.json (e.g. lost+found)
		credPath := filepath.Join(dir, "credentials.json")
		if _, err := os.Stat(credPath); os.IsNotExist(err) {
			slog.Debug("skipping directory without credentials.json", "id", id)
			continue
		}
		client, err := NewClient(dir)
		if err != nil {
			slog.Warn("skipping account directory", "id", id, "error", err)
			continue
		}
		am.accounts[id] = client
	}

	return am, nil
}

// GetClient returns the client for a given account ID.
func (am *AccountManager) GetClient(accountID string) (*Client, bool) {
	am.mu.RLock()
	defer am.mu.RUnlock()
	c, ok := am.accounts[accountID]
	return c, ok
}

// AddAccount adds an authenticated account.
func (am *AccountManager) AddAccount(id string, client *Client) {
	am.mu.Lock()
	defer am.mu.Unlock()
	am.accounts[id] = client
}

// RemoveAccount removes an account and deletes its storage directory.
func (am *AccountManager) RemoveAccount(id string) error {
	am.mu.Lock()
	defer am.mu.Unlock()
	if _, ok := am.accounts[id]; !ok {
		return fmt.Errorf("account %s not found", id)
	}
	delete(am.accounts, id)
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
			ID: id,
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
}

// EnsureAllAuthenticated authenticates all existing accounts at startup.
func (am *AccountManager) EnsureAllAuthenticated(ctx context.Context) error {
	am.mu.RLock()
	defer am.mu.RUnlock()
	for id, client := range am.accounts {
		slog.Info("authenticating account", "id", id, "username", client.creds.GitHubUsername)
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
	client         *Client
	AccountID      string
	GitHubUsername string
}

func NewAccountTokenProvider(client *Client) *AccountTokenProvider {
	return &AccountTokenProvider{
		client:         client,
		GitHubUsername: client.creds.GitHubUsername,
	}
}

func (p *AccountTokenProvider) GetToken(ctx context.Context) (string, error) {
	return p.client.GetToken(ctx)
}

func (p *AccountTokenProvider) GetBaseURL() string {
	return p.client.GetBaseURL()
}

// GetAccountInfo implements upstream.AccountInfoProvider.
func (p *AccountTokenProvider) GetAccountInfo() (accountID, username string) {
	return p.AccountID, p.GitHubUsername
}
