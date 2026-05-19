package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// FileBackend implements Backend using local filesystem (original behavior).
type FileBackend struct {
	baseDir string
}

func NewFileBackend(baseDir string) (*FileBackend, error) {
	if err := os.MkdirAll(baseDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create base directory: %w", err)
	}
	return &FileBackend{baseDir: baseDir}, nil
}

func (f *FileBackend) ListAccounts(_ context.Context) ([]string, error) {
	entries, err := os.ReadDir(f.baseDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read base directory: %w", err)
	}
	var ids []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		credPath := filepath.Join(f.baseDir, entry.Name(), "credentials.json")
		if _, err := os.Stat(credPath); os.IsNotExist(err) {
			continue
		}
		ids = append(ids, entry.Name())
	}
	return ids, nil
}

func (f *FileBackend) LoadCredentials(_ context.Context, accountID string) (*Credentials, error) {
	path := filepath.Join(f.baseDir, accountID, "credentials.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Credentials{}, nil
		}
		return nil, fmt.Errorf("failed to read credentials: %w", err)
	}

	// Parse the file format (which includes copilot_token as object)
	var raw struct {
		GitHubToken    string          `json:"github_token"`
		GitHubUsername string          `json:"github_username,omitempty"`
		CopilotToken   json.RawMessage `json:"copilot_token,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse credentials: %w", err)
	}

	return &Credentials{
		GitHubToken:    raw.GitHubToken,
		GitHubUsername: raw.GitHubUsername,
		CopilotToken:   raw.CopilotToken,
	}, nil
}

func (f *FileBackend) SaveCredentials(_ context.Context, accountID string, creds *Credentials) error {
	dir := filepath.Join(f.baseDir, accountID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create account directory: %w", err)
	}

	// Reconstruct the original file format
	raw := map[string]interface{}{
		"github_token": creds.GitHubToken,
	}
	if creds.GitHubUsername != "" {
		raw["github_username"] = creds.GitHubUsername
	}
	if len(creds.CopilotToken) > 0 {
		var ct interface{}
		json.Unmarshal(creds.CopilotToken, &ct)
		raw["copilot_token"] = ct
	}

	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal credentials: %w", err)
	}

	path := filepath.Join(dir, "credentials.json")
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to rename: %w", err)
	}
	return nil
}

func (f *FileBackend) DeleteAccount(_ context.Context, accountID string) error {
	dir := filepath.Join(f.baseDir, accountID)
	return os.RemoveAll(dir)
}

func (f *FileBackend) CreateAccount(_ context.Context, accountID string) error {
	dir := filepath.Join(f.baseDir, accountID)
	return os.MkdirAll(dir, 0700)
}

// BaseDir returns the base directory (needed for backward compat).
func (f *FileBackend) BaseDir() string {
	return f.baseDir
}
