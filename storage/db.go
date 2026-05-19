package storage

import (
	"context"
	"fmt"
	"os"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Account is the GORM model for the accounts table.
type Account struct {
	ID        string `gorm:"primaryKey;size:64"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// AccountCredential is the GORM model for encrypted credential storage.
type AccountCredential struct {
	AccountID      string `gorm:"primaryKey;size:64"`
	GitHubToken    string `gorm:"type:text"` // encrypted
	GitHubUsername string `gorm:"size:255"`
	CopilotToken   string `gorm:"type:text"` // encrypted JSON
	UpdatedAt      time.Time
}

// DBBackend implements Backend using PostgreSQL via GORM.
type DBBackend struct {
	db        *gorm.DB
	encryptor *Encryptor
}

// NewDBBackend creates a new PostgreSQL storage backend.
// It auto-migrates the schema on startup.
func NewDBBackend(encryptor *Encryptor) (*DBBackend, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		host := envOrDefault("DB_HOST", "localhost")
		port := envOrDefault("DB_PORT", "5432")
		dbname := envOrDefault("DB_NAME", "copilot2api")
		user := envOrDefault("DB_USER", "copilot2api")
		password := os.Getenv("DB_PASSWORD")
		sslmode := envOrDefault("DB_SSLMODE", "require")
		dsn = fmt.Sprintf("host=%s port=%s dbname=%s user=%s password=%s sslmode=%s",
			host, port, dbname, user, password, sslmode)
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	// Configure connection pool
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get sql.DB: %w", err)
	}
	sqlDB.SetMaxOpenConns(10)
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)

	// Auto-migrate
	if err := db.AutoMigrate(&Account{}, &AccountCredential{}); err != nil {
		return nil, fmt.Errorf("failed to migrate schema: %w", err)
	}

	return &DBBackend{db: db, encryptor: encryptor}, nil
}

func (d *DBBackend) ListAccounts(ctx context.Context) ([]string, error) {
	var accounts []Account
	if err := d.db.WithContext(ctx).Find(&accounts).Error; err != nil {
		return nil, err
	}
	ids := make([]string, len(accounts))
	for i, a := range accounts {
		ids[i] = a.ID
	}
	return ids, nil
}

func (d *DBBackend) LoadCredentials(ctx context.Context, accountID string) (*Credentials, error) {
	var cred AccountCredential
	err := d.db.WithContext(ctx).Where("account_id = ?", accountID).First(&cred).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return &Credentials{}, nil
		}
		return nil, err
	}

	// Decrypt sensitive fields
	githubToken := ""
	if cred.GitHubToken != "" {
		decrypted, err := d.encryptor.Decrypt(cred.GitHubToken)
		if err != nil {
			return nil, fmt.Errorf("failed to decrypt github_token: %w", err)
		}
		githubToken = string(decrypted)
	}

	var copilotToken []byte
	if cred.CopilotToken != "" {
		decrypted, err := d.encryptor.Decrypt(cred.CopilotToken)
		if err != nil {
			return nil, fmt.Errorf("failed to decrypt copilot_token: %w", err)
		}
		copilotToken = decrypted
	}

	return &Credentials{
		GitHubToken:    githubToken,
		GitHubUsername: cred.GitHubUsername,
		CopilotToken:   copilotToken,
	}, nil
}

func (d *DBBackend) SaveCredentials(ctx context.Context, accountID string, creds *Credentials) error {
	// Encrypt sensitive fields
	encGitHubToken := ""
	if creds.GitHubToken != "" {
		encrypted, err := d.encryptor.Encrypt([]byte(creds.GitHubToken))
		if err != nil {
			return fmt.Errorf("failed to encrypt github_token: %w", err)
		}
		encGitHubToken = encrypted
	}

	encCopilotToken := ""
	if len(creds.CopilotToken) > 0 {
		encrypted, err := d.encryptor.Encrypt(creds.CopilotToken)
		if err != nil {
			return fmt.Errorf("failed to encrypt copilot_token: %w", err)
		}
		encCopilotToken = encrypted
	}

	cred := AccountCredential{
		AccountID:      accountID,
		GitHubToken:    encGitHubToken,
		GitHubUsername: creds.GitHubUsername,
		CopilotToken:   encCopilotToken,
		UpdatedAt:      time.Now(),
	}

	// Upsert
	result := d.db.WithContext(ctx).Where("account_id = ?", accountID).First(&AccountCredential{})
	if result.Error == gorm.ErrRecordNotFound {
		return d.db.WithContext(ctx).Create(&cred).Error
	}
	return d.db.WithContext(ctx).Where("account_id = ?", accountID).Updates(&cred).Error
}

func (d *DBBackend) DeleteAccount(ctx context.Context, accountID string) error {
	return d.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("account_id = ?", accountID).Delete(&AccountCredential{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", accountID).Delete(&Account{}).Error
	})
}

func (d *DBBackend) CreateAccount(ctx context.Context, accountID string) error {
	return d.db.WithContext(ctx).Create(&Account{ID: accountID}).Error
}

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
