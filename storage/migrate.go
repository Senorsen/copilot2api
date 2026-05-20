package storage

import (
	"context"
	"fmt"
	"log/slog"
)

// MigrateFileToDB imports all accounts and credentials from a FileBackend into a DBBackend.
// It is idempotent: existing accounts in DB are updated, not duplicated.
// Returns the number of accounts migrated.
func MigrateFileToDB(ctx context.Context, file *FileBackend, db *DBBackend) (int, error) {
	accounts, err := file.ListAccounts(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to list file accounts: %w", err)
	}

	if len(accounts) == 0 {
		slog.Info("migrate: no accounts found in file backend, nothing to import")
		return 0, nil
	}

	slog.Info("migrate: starting file→DB import", "accounts", len(accounts))

	migrated := 0
	for _, accountID := range accounts {
		// Ensure account exists in DB
		if err := db.CreateAccount(ctx, accountID); err != nil {
			// Account may already exist, try to continue
			slog.Warn("migrate: account may already exist", "accountID", accountID, "error", err)
		}

		// Load credentials from file
		creds, err := file.LoadCredentials(ctx, accountID)
		if err != nil {
			slog.Error("migrate: failed to load credentials from file", "accountID", accountID, "error", err)
			continue
		}

		// Skip empty credentials
		if creds.GitHubToken == "" && len(creds.CopilotToken) == 0 {
			slog.Info("migrate: skipping account with no credentials", "accountID", accountID)
			continue
		}

		// Save to DB (encrypted)
		if err := db.SaveCredentials(ctx, accountID, creds); err != nil {
			return migrated, fmt.Errorf("migrate: failed to save credentials to DB for %s: %w", accountID, err)
		}

		slog.Info("migrate: imported account", "accountID", accountID, "username", creds.GitHubUsername)
		migrated++
	}

	slog.Info("migrate: file→DB import complete", "migrated", migrated, "total", len(accounts))
	return migrated, nil
}
