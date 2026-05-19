package control

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/whtsky/copilot2api/auth"
	"github.com/whtsky/copilot2api/storage"
)

// generateUUID generates a random UUID v4 string.
func generateUUID() string {
	var buf [16]byte
	_, _ = rand.Read(buf[:])
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}

// Server is the control plane HTTP server (port 7778).
type Server struct {
	am         *auth.AccountManager
	adminToken string
	mu         sync.Mutex
	flows      map[string]*pendingFlow
}

type pendingFlow struct {
	DeviceCode      string `json:"-"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	Interval        int    `json:"-"`
	ExpiresIn       int    `json:"-"`
	AccountID       string `json:"account_id,omitempty"`
	GitHubUsername  string `json:"github_username,omitempty"`
	UsernameSuffix  string `json:"-"`
	Status          string `json:"status"`
	Error           string `json:"error,omitempty"`
	cancel          context.CancelFunc
}

func NewServer(am *auth.AccountManager, adminToken string) *Server {
	return &Server{
		am:         am,
		adminToken: adminToken,
		flows:      make(map[string]*pendingFlow),
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/accounts/login", s.handleLogin)
	mux.HandleFunc("/accounts", s.handleListAccounts)
	mux.Handle("/accounts/", http.HandlerFunc(s.handleAccountRoute))
	return s.authMiddleware(mux)
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.adminToken == "" {
			next.ServeHTTP(w, r)
			return
		}
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing or invalid Authorization header"})
			return
		}
		token := strings.TrimPrefix(authHeader, "Bearer ")
		if token != s.adminToken {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid token"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	var reqBody struct {
		UsernameSuffix string `json:"username_suffix"`
	}
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&reqBody)
	}

	deviceResp, err := auth.InitiateDeviceFlow()
	if err != nil {
		slog.Error("failed to initiate device flow", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to initiate device flow"})
		return
	}

	progressID := fmt.Sprintf("%d", time.Now().UnixNano())

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(deviceResp.ExpiresIn)*time.Second)

	flow := &pendingFlow{
		DeviceCode:      deviceResp.DeviceCode,
		UserCode:        deviceResp.UserCode,
		VerificationURI: deviceResp.VerificationURI,
		Interval:        deviceResp.Interval,
		ExpiresIn:       deviceResp.ExpiresIn,
		UsernameSuffix:  reqBody.UsernameSuffix,
		Status:          "pending",
		cancel:          cancel,
	}

	s.mu.Lock()
	s.flows[progressID] = flow
	s.mu.Unlock()

	go s.pollDeviceFlow(ctx, progressID, flow)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"progress_id":      progressID,
		"user_code":        deviceResp.UserCode,
		"verification_uri": deviceResp.VerificationURI,
		"expires_in":       deviceResp.ExpiresIn,
	})
}

func (s *Server) pollDeviceFlow(ctx context.Context, progressID string, flow *pendingFlow) {
	defer flow.cancel()

	accessToken, err := auth.PollForAccessToken(flow.DeviceCode, flow.Interval, time.Duration(flow.ExpiresIn)*time.Second)
	if err != nil {
		s.mu.Lock()
		if strings.Contains(err.Error(), "timeout") {
			flow.Status = "expired"
		} else {
			flow.Status = "error"
			flow.Error = err.Error()
		}
		s.mu.Unlock()
		return
	}

	username, err := auth.FetchGitHubUsername(accessToken)
	if err != nil {
		slog.Warn("failed to fetch GitHub username", "error", err)
		username = ""
	}

	if flow.UsernameSuffix != "" && username != "" {
		if !strings.HasSuffix(username, flow.UsernameSuffix) {
			s.mu.Lock()
			flow.Status = "error"
			flow.Error = fmt.Sprintf("username %q does not end with required suffix %q", username, flow.UsernameSuffix)
			flow.GitHubUsername = username
			s.mu.Unlock()
			return
		}
	}

	accountID := generateUUID()
	backend := s.am.Backend()

	// Create account in backend
	if err := backend.CreateAccount(ctx, accountID); err != nil {
		s.mu.Lock()
		flow.Status = "error"
		flow.Error = "failed to create account: " + err.Error()
		s.mu.Unlock()
		return
	}

	// Save credentials
	creds := &storage.Credentials{
		GitHubToken:    accessToken,
		GitHubUsername: username,
	}
	if err := backend.SaveCredentials(ctx, accountID, creds); err != nil {
		s.mu.Lock()
		flow.Status = "error"
		flow.Error = "failed to save credentials"
		s.mu.Unlock()
		return
	}

	// Create client from stored credentials
	client, err := auth.NewClientFromCredentials(accountID, creds, backend)
	if err != nil {
		s.mu.Lock()
		flow.Status = "error"
		flow.Error = "failed to create client"
		s.mu.Unlock()
		return
	}
	if err := client.EnsureAuthenticated(ctx); err != nil {
		s.mu.Lock()
		flow.Status = "error"
		flow.Error = "failed to authenticate: " + err.Error()
		s.mu.Unlock()
		return
	}

	s.am.AddAccount(accountID, client)

	s.mu.Lock()
	flow.Status = "completed"
	flow.AccountID = accountID
	flow.GitHubUsername = username
	s.mu.Unlock()

	slog.Info("new account added via control plane", "account_id", accountID)
}

func (s *Server) handleAccountRoute(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/accounts/")
	parts := strings.SplitN(path, "/", 2)
	id := parts[0]

	if len(parts) == 2 && parts[1] == "status" {
		s.handleFlowStatus(w, r, id)
		return
	}

	if r.Method == http.MethodDelete {
		s.handleDeleteAccount(w, r, id)
		return
	}

	writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
}

func (s *Server) handleFlowStatus(w http.ResponseWriter, r *http.Request, progressID string) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	s.mu.Lock()
	flow, ok := s.flows[progressID]
	s.mu.Unlock()

	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "progress not found"})
		return
	}

	resp := map[string]interface{}{
		"status": flow.Status,
	}
	if flow.AccountID != "" {
		resp["account_id"] = flow.AccountID
	}
	if flow.GitHubUsername != "" {
		resp["github_username"] = flow.GitHubUsername
	}
	if flow.Error != "" {
		resp["error"] = flow.Error
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleListAccounts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	accounts := s.am.ListAccounts()
	if accounts == nil {
		accounts = []auth.AccountInfo{}
	}
	writeJSON(w, http.StatusOK, accounts)
}

func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request, accountID string) {
	if err := s.am.RemoveAccount(accountID); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	slog.Info("account deleted via control plane", "account_id", accountID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
