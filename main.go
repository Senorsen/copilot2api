package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/whtsky/copilot2api/anthropic"
	"github.com/whtsky/copilot2api/auth"
	"github.com/whtsky/copilot2api/control"
	"github.com/whtsky/copilot2api/gemini"
	"github.com/whtsky/copilot2api/internal/models"
	"github.com/whtsky/copilot2api/internal/upstream"
	"github.com/whtsky/copilot2api/proxy"
)

var version = "dev"

func main() {
	var (
		port        = flag.Int("port", 0, "Server port (env: COPILOT2API_PORT, default: 7777)")
		controlPort = flag.Int("control-port", 0, "Control plane port (env: COPILOT2API_CONTROL_PORT, default: 7778)")
		host        = flag.String("host", "", "Server host (env: COPILOT2API_HOST, default: 127.0.0.1)")
		tokenDir    = flag.String("token-dir", "", "Token storage directory (env: COPILOT2API_TOKEN_DIR, default: ~/.config/copilot2api)")
		showVersion = flag.Bool("version", false, "Show version and exit")
		debug       = flag.Bool("debug", false, "Enable debug logging (env: COPILOT2API_DEBUG)")
	)
	flag.Parse()

	// Apply debug env var
	if !*debug {
		if v := os.Getenv("COPILOT2API_DEBUG"); v != "" {
			if enabled, err := strconv.ParseBool(v); err == nil {
				*debug = enabled
			}
		}
	}

	// Apply env var defaults
	if *host == "" {
		if v := os.Getenv("COPILOT2API_HOST"); v != "" {
			*host = v
		} else {
			*host = "127.0.0.1"
		}
	}
	if *port == 0 {
		if v := os.Getenv("COPILOT2API_PORT"); v != "" {
			if p, err := strconv.Atoi(v); err == nil {
				*port = p
			}
		}
		if *port == 0 {
			*port = 7777
		}
	}
	if *controlPort == 0 {
		if v := os.Getenv("COPILOT2API_CONTROL_PORT"); v != "" {
			if p, err := strconv.Atoi(v); err == nil {
				*controlPort = p
			}
		}
		if *controlPort == 0 {
			*controlPort = 7778
		}
	}

	if *showVersion {
		fmt.Printf("copilot2api version %s\n", version)
		os.Exit(0)
	}

	// Set up logging
	logLevel := slog.LevelInfo
	if *debug {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	}))
	slog.SetDefault(logger)

	// Determine token directory
	if *tokenDir == "" {
		if v := os.Getenv("COPILOT2API_TOKEN_DIR"); v != "" {
			*tokenDir = v
		} else {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				slog.Error("failed to get home directory", "error", err)
				os.Exit(1)
			}
			*tokenDir = filepath.Join(homeDir, ".config", "copilot2api")
		}
	}

	// Initialize account manager
	accountManager, err := auth.NewAccountManager(*tokenDir)
	if err != nil {
		slog.Error("failed to initialize account manager", "error", err)
		os.Exit(1)
	}
	slog.Info("initialized accounts", "count", accountManager.Count())

	// Authenticate all existing accounts at startup
	ctx := context.Background()
	if accountManager.Count() > 0 {
		if err := accountManager.EnsureAllAuthenticated(ctx); err != nil {
			slog.Error("authentication failed", "error", err)
			os.Exit(1)
		}
	}

	// Shared HTTP transport
	transport := upstream.NewTransport()

	// Models cache — use first available account for fetching models list
	var modelsCache *models.Cache
	accounts := accountManager.ListAccounts()
	if len(accounts) > 0 {
		if client, ok := accountManager.GetClient(accounts[0].ID); ok {
			tp := auth.NewAccountTokenProvider(client)
			upstreamClient := upstream.NewClient(tp, transport)
			modelsCache = models.NewCache(upstreamClient, 5*time.Minute)
		}
	}
	if modelsCache == nil {
		modelsCache = models.NewCache(nil, 5*time.Minute)
	}

	// Set up proxy mux with path-based routing
	mux := http.NewServeMux()

	// All API routes require account_id: /v1/{account_id}/...
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		handleAccountRoute(w, r, accountManager, transport, modelsCache)
	})

	// Gemini routes also require account_id: /v1beta/{account_id}/models/...
	mux.HandleFunc("/v1beta/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v1beta/")
		parts := strings.SplitN(path, "/", 2)
		if len(parts) < 2 {
			proxy.WriteOpenAIError(w, http.StatusBadRequest, proxy.OpenAIErrorTypeInvalidRequest, "account_id required in path: /v1beta/{account_id}/models/...")
			return
		}
		accountID := parts[0]
		client, ok := accountManager.GetClient(accountID)
		if !ok {
			proxy.WriteOpenAIError(w, http.StatusNotFound, proxy.OpenAIErrorTypeInvalidRequest, fmt.Sprintf("account %q not found", accountID))
			return
		}
		r.URL.Path = "/v1beta/" + parts[1]
		tp := auth.NewAccountTokenProvider(client)
		handler := gemini.NewHandler(tp, transport, modelsCache)
		handler.ServeHTTP(w, r)
	})

	// Usage endpoint (aggregates all accounts)
	mux.HandleFunc("/usage", func(w http.ResponseWriter, r *http.Request) {
		// Pick first available account for usage
		accounts := accountManager.ListAccounts()
		if len(accounts) == 0 {
			proxy.WriteOpenAIError(w, http.StatusServiceUnavailable, proxy.OpenAIErrorTypeServerError, "no accounts configured")
			return
		}
		client, _ := accountManager.GetClient(accounts[0].ID)
		tp := auth.NewAccountTokenProvider(client)
		proxyHandler := proxy.NewHandler(tp, transport, modelsCache, &aggregateUsageProvider{am: accountManager})
		proxyHandler.HandleUsage(w, r)
	})


	// Create proxy server
	proxyServer := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", *host, *port),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		Handler:           logAllRequests(mux),
	}

	// Create control plane server
	adminToken := os.Getenv("ADMIN_TOKEN")
	controlServer := control.NewServer(accountManager, adminToken)
	controlHTTP := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", *host, *controlPort),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
		Handler:           controlServer.Handler(),
	}

	// Start servers
	serverErr := make(chan error, 2)
	go func() {
		slog.Info("starting proxy server", "host", *host, "port", *port)
		if err := proxyServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()
	go func() {
		slog.Info("starting control plane server", "host", *host, "port", *controlPort)
		if err := controlHTTP.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	// Wait for interrupt or error
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-quit:
	case err := <-serverErr:
		slog.Error("server failed", "error", err)
		os.Exit(1)
	}

	slog.Info("shutting down servers")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	proxyServer.Shutdown(shutdownCtx)
	controlHTTP.Shutdown(shutdownCtx)
	slog.Info("servers stopped")
}

// handleAccountRoute routes /v1/{account_id}/... — account_id is always required.
func handleAccountRoute(w http.ResponseWriter, r *http.Request, am *auth.AccountManager, transport *http.Transport, mc *models.Cache) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/")

	// Parse as /v1/{account_id}/...
	parts := strings.SplitN(path, "/", 2)
	if len(parts) < 2 || parts[1] == "" {
		proxy.WriteOpenAIError(w, http.StatusBadRequest, proxy.OpenAIErrorTypeInvalidRequest, "account_id required in path: /v1/{account_id}/chat/completions")
		return
	}

	accountID := parts[0]
	remainder := parts[1]

	client, ok := am.GetClient(accountID)
	if !ok {
		proxy.WriteOpenAIError(w, http.StatusNotFound, proxy.OpenAIErrorTypeInvalidRequest, fmt.Sprintf("account %q not found", accountID))
		return
	}

	// Rewrite path to /v1/{remainder}
	r.URL.Path = "/v1/" + remainder
	tp := auth.NewAccountTokenProvider(client)
	handleWithTokenProvider(w, r, tp, transport, mc)
}

// handleWithTokenProvider dispatches a request using a specific token provider.
func handleWithTokenProvider(w http.ResponseWriter, r *http.Request, tp upstream.TokenProvider, transport *http.Transport, mc *models.Cache) {
	path := r.URL.Path

	switch {
	case path == "/v1/messages" || strings.HasPrefix(path, "/v1/messages"):
		handler := anthropic.NewHandler(tp, transport, mc)
		handler.ServeHTTP(w, r)
	case strings.HasPrefix(path, "/v1beta/models"):
		handler := gemini.NewHandler(tp, transport, mc)
		handler.ServeHTTP(w, r)
	default:
		handler := proxy.NewHandler(tp, transport, mc, nil)
		handler.ServeHTTP(w, r)
	}
}

// aggregateUsageProvider implements proxy.UsageProvider for all accounts.
type aggregateUsageProvider struct {
	am *auth.AccountManager
}

func (a *aggregateUsageProvider) GetUsageInfo(ctx context.Context) (interface{}, error) {
	accounts := a.am.ListAccounts()
	var results []interface{}
	for _, acc := range accounts {
		client, ok := a.am.GetClient(acc.ID)
		if !ok {
			continue
		}
		info, err := client.GetUsageInfo(ctx)
		if err != nil {
			continue
		}
		results = append(results, info)
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("no usage info available")
	}
	if len(results) == 1 {
		return results[0], nil
	}
	return results, nil
}

func logAllRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slog.Debug("incoming request", "method", r.Method, "path", r.URL.Path, "query", r.URL.RawQuery)
		next.ServeHTTP(w, r)
	})
}
