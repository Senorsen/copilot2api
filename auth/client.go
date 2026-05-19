package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/whtsky/copilot2api/internal/copilot"
	"github.com/whtsky/copilot2api/storage"
)

type Client struct {
	accountID string
	backend   storage.Backend
	storage   *TokenStorage // legacy file storage (nil when using backend)
	mu        sync.RWMutex
	creds     *StoredCredentials
	refreshMu sync.Mutex
}

// NewClient creates a new auth client from a token directory (legacy file mode).
func NewClient(tokenDir string) (*Client, error) {
	st, err := NewTokenStorage(tokenDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create token storage: %w", err)
	}

	creds, err := st.LoadCredentials()
	if err != nil {
		return nil, fmt.Errorf("failed to load credentials: %w", err)
	}

	return &Client{
		storage: st,
		creds:   creds,
	}, nil
}

// GetValidToken returns a valid Copilot token, performing authentication if necessary
func (c *Client) GetValidToken(ctx context.Context) (*CopilotToken, error) {
	c.mu.RLock()
	if c.creds.CopilotToken != nil && c.creds.CopilotToken.IsTokenUsable() {
		token := c.creds.CopilotToken
		c.mu.RUnlock()
		return token, nil
	}
	c.mu.RUnlock()

	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()

	c.mu.RLock()
	if c.creds.CopilotToken != nil && c.creds.CopilotToken.IsTokenUsable() {
		token := c.creds.CopilotToken
		c.mu.RUnlock()
		return token, nil
	}
	c.mu.RUnlock()

	c.mu.RLock()
	hasGitHubToken := c.creds.GitHubToken != ""
	c.mu.RUnlock()

	if hasGitHubToken {
		if err := c.refreshCopilotToken(); err == nil {
			c.mu.RLock()
			token := c.creds.CopilotToken
			c.mu.RUnlock()
			return token, nil
		} else {
			slog.Error("failed to refresh copilot token with stored GitHub token", "error", err, "username", c.creds.GitHubUsername)
		}
	}

	return nil, fmt.Errorf("authentication required: no valid GitHub token available (run device flow at startup)")
}

// GetToken returns a valid Copilot bearer token string.
func (c *Client) GetToken(ctx context.Context) (string, error) {
	tok, err := c.GetValidToken(ctx)
	if err != nil {
		return "", err
	}
	return tok.Token, nil
}

// GetBaseURL returns the base URL for API calls
func (c *Client) GetBaseURL() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.creds.CopilotToken != nil {
		return c.creds.CopilotToken.BaseURL
	}
	return DefaultBaseURL
}

// EnsureAuthenticated runs the interactive device flow if needed and verifies
// that a valid Copilot token can be obtained.
func (c *Client) EnsureAuthenticated(ctx context.Context) error {
	if err := c.RunDeviceFlowIfNeeded(); err != nil {
		return fmt.Errorf("device flow failed: %w", err)
	}
	if _, err := c.GetValidToken(ctx); err != nil {
		return fmt.Errorf("failed to obtain valid token: %w", err)
	}
	return nil
}

// RunDeviceFlowIfNeeded performs the interactive OAuth device flow if no
// valid GitHub token is stored.
func (c *Client) RunDeviceFlowIfNeeded() error {
	c.mu.RLock()
	hasGitHubToken := c.creds.GitHubToken != ""
	c.mu.RUnlock()

	if hasGitHubToken {
		return nil
	}

	return c.performDeviceFlow()
}

func (c *Client) performDeviceFlow() error {
	slog.Info("starting GitHub Device Flow OAuth")

	deviceResp, err := InitiateDeviceFlow()
	if err != nil {
		return fmt.Errorf("failed to initiate device flow: %w", err)
	}

	fmt.Printf("\n🔐 GitHub Authentication Required\n")
	fmt.Printf("Please visit: %s\n", deviceResp.VerificationURI)
	fmt.Printf("Enter code: %s\n\n", deviceResp.UserCode)
	fmt.Printf("Waiting for authorization...")

	timeout := time.Duration(deviceResp.ExpiresIn) * time.Second
	accessToken, err := PollForAccessToken(deviceResp.DeviceCode, deviceResp.Interval, timeout)
	if err != nil {
		return fmt.Errorf("failed to get access token: %w", err)
	}

	fmt.Printf("\n✅ Authentication successful!\n\n")

	c.mu.Lock()
	c.creds.GitHubToken = accessToken
	c.mu.Unlock()

	if err := c.refreshCopilotToken(); err != nil {
		return fmt.Errorf("failed to get copilot token: %w", err)
	}

	return nil
}

func (c *Client) refreshCopilotToken() error {
	start := time.Now()
	c.mu.RLock()
	username := c.creds.GitHubUsername
	githubToken := c.creds.GitHubToken
	c.mu.RUnlock()

	slog.Info("refreshing Copilot token", "username", username)

	copilotToken, err := GetCopilotToken(githubToken)
	if err != nil {
		return fmt.Errorf("failed to get copilot token: %w", err)
	}

	c.mu.Lock()
	c.creds.CopilotToken = copilotToken
	credsCopy := *c.creds
	c.mu.Unlock()

	// Persist credentials
	c.saveCredentials(&credsCopy)

	slog.Info("copilot token refreshed", "expires_at", copilotToken.ExpiresAt, "base_url", copilotToken.BaseURL, "duration_ms", time.Since(start).Milliseconds(), "username", username)
	return nil
}

func (c *Client) saveCredentials(creds *StoredCredentials) {
	// Try new backend first
	if c.backend != nil && c.accountID != "" {
		ctJSON, _ := json.Marshal(creds.CopilotToken)
		storageCreds := &storage.Credentials{
			GitHubToken:    creds.GitHubToken,
			GitHubUsername: creds.GitHubUsername,
			CopilotToken:   ctJSON,
		}
		if err := c.backend.SaveCredentials(context.Background(), c.accountID, storageCreds); err != nil {
			slog.Warn("failed to save credentials to backend", "error", err, "account_id", c.accountID)
		}
		return
	}

	// Legacy file storage
	if c.storage != nil {
		if err := c.storage.SaveCredentials(creds); err != nil {
			slog.Warn("failed to save credentials", "error", err)
		}
	}
}

// UsageInfo contains Copilot usage and quota information
type UsageInfo struct {
	SKU                  string      `json:"sku"`
	Individual           bool        `json:"individual"`
	LimitedUserQuotas    interface{} `json:"limited_user_quotas"`
	LimitedUserResetDate interface{} `json:"limited_user_reset_date"`
	EnterpriseList       []int       `json:"enterprise_list,omitempty"`
	OrganizationList     []string    `json:"organization_list,omitempty"`
}

// GetUsageInfo fetches usage info from the Copilot token endpoint
func (c *Client) GetUsageInfo(ctx context.Context) (*UsageInfo, error) {
	c.mu.RLock()
	githubToken := c.creds.GitHubToken
	c.mu.RUnlock()

	if githubToken == "" {
		return nil, fmt.Errorf("no GitHub token available")
	}

	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/copilot_internal/v2/token", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+githubToken)
	req.Header.Set("User-Agent", copilot.CopilotUserAgent)

	resp, err := sharedHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("usage request failed with status %d", resp.StatusCode)
	}

	var info UsageInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, err
	}

	return &info, nil
}
