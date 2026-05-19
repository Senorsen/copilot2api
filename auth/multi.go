package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/whtsky/copilot2api/storage"
)

// AccountManager manages multiple auth accounts with ID-based lookup.
type AccountManager struct {
	mu       sync.RWMutex
	accounts map[string]*Client // account_id -> client
	backend  storage.Backend
	baseDir  string // only used for file backend compat
}

// NewAccountManager creates an account manager backed by the given storage backend.
func NewAccountManager(backend storage.Backend) (*AccountManager, error) {
	am := &AccountManager{
		accounts: make(map[string]*Client),
		backend:  backend,
	}

	// If it's a file backend, store baseDir for backward compat
	if fb, ok := backend.(*storage.FileBackend); ok {
		am.baseDir = fb.BaseDir()
	}

	ctx := context.Background()
	ids, err := backend.ListAccounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list accounts: %w", err)
	}

	for _, id := range ids {
		creds, err := backend.LoadCredentials(ctx, id)
		if err != nil {
			slog.Warn("skipping account", "id", id, "error", err)
			continue
		}
		client, err := NewClientFromCredentials(id, creds, backend)
		if err != nil {
			slog.Warn("skipping account", "id", id, "error", err)
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

// RemoveAccount removes an account and deletes its storage.
func (am *AccountManager) RemoveAccount(id string) error {
	am.mu.Lock()
	defer am.mu.Unlock()
	if _, ok := am.accounts[id]; !ok {
		return fmt.Errorf("account %s not found", id)
	}
	delete(am.accounts, id)
	return am.backend.DeleteAccount(context.Background(), id)
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

// BaseDir returns the base directory for account storage (file backend only).
func (am *AccountManager) BaseDir() string {
	return am.baseDir
}

// Backend returns the storage backend.
func (am *AccountManager) Backend() storage.Backend {
	return am.backend
}

// NewClientFromCredentials creates a Client from storage credentials.
func NewClientFromCredentials(accountID string, creds *storage.Credentials, backend storage.Backend) (*Client, error) {
	stored := &StoredCredentials{
		GitHubToken:    creds.GitHubToken,
		GitHubUsername: creds.GitHubUsername,
	}

	// Decode CopilotToken if present
	if len(creds.CopilotToken) > 0 {
		var ct CopilotToken
		if err := json.Unmarshal(creds.CopilotToken, &ct); err == nil {
			stored.CopilotToken = &ct
		}
	}

	return &Client{
		accountID: accountID,
		backend:   backend,
		creds:     stored,
	}, nil
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
