-- copilot2api PostgreSQL schema
-- Run this to initialize the database before starting with STORAGE=db

CREATE TABLE IF NOT EXISTS accounts (
    id VARCHAR(64) PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS account_credentials (
    account_id VARCHAR(64) PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    github_token TEXT NOT NULL DEFAULT '',       -- AES-256-GCM encrypted, base64 encoded
    github_username VARCHAR(255) NOT NULL DEFAULT '',
    copilot_token TEXT NOT NULL DEFAULT '',      -- AES-256-GCM encrypted JSON, base64 encoded
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_account_credentials_username ON account_credentials(github_username);
