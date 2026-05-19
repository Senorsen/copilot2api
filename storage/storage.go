// Package storage defines the interface for credential persistence backends.
package storage

import (
	"context"
)

// Credentials represents stored account credentials.
type Credentials struct {
	GitHubToken    string `json:"github_token"`
	GitHubUsername string `json:"github_username,omitempty"`
	CopilotToken   []byte `json:"copilot_token,omitempty"` // JSON-encoded CopilotToken
}

// Backend is the interface that storage implementations must satisfy.
type Backend interface {
	// ListAccounts returns all stored account IDs.
	ListAccounts(ctx context.Context) ([]string, error)

	// LoadCredentials loads credentials for an account.
	LoadCredentials(ctx context.Context, accountID string) (*Credentials, error)

	// SaveCredentials persists credentials for an account.
	SaveCredentials(ctx context.Context, accountID string, creds *Credentials) error

	// DeleteAccount removes an account and all its data.
	DeleteAccount(ctx context.Context, accountID string) error

	// CreateAccount creates a new account slot. Returns error if already exists.
	CreateAccount(ctx context.Context, accountID string) error
}
