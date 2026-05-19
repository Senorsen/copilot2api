package storage

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"
)

// GetMasterKey retrieves the master encryption key based on ENCRYPTION_KEY_SOURCE.
func GetMasterKey() (string, error) {
	source := os.Getenv("ENCRYPTION_KEY_SOURCE")
	if source == "" {
		source = "env"
	}

	switch source {
	case "env":
		key := os.Getenv("MASTER_KEY")
		if key == "" {
			return "", fmt.Errorf("MASTER_KEY environment variable is required when ENCRYPTION_KEY_SOURCE=env")
		}
		return key, nil
	case "keyvault":
		return getMasterKeyFromKeyVault()
	default:
		return "", fmt.Errorf("unsupported ENCRYPTION_KEY_SOURCE: %s", source)
	}
}

func getMasterKeyFromKeyVault() (string, error) {
	vaultURL := os.Getenv("AZURE_KEYVAULT_URL")
	if vaultURL == "" {
		return "", fmt.Errorf("AZURE_KEYVAULT_URL is required when ENCRYPTION_KEY_SOURCE=keyvault")
	}
	secretName := os.Getenv("AZURE_KEYVAULT_SECRET_NAME")
	if secretName == "" {
		return "", fmt.Errorf("AZURE_KEYVAULT_SECRET_NAME is required when ENCRYPTION_KEY_SOURCE=keyvault")
	}

	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		return "", fmt.Errorf("failed to create Azure credential: %w", err)
	}

	client, err := azsecrets.NewClient(vaultURL, cred, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create Key Vault client: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := client.GetSecret(ctx, secretName, "", nil)
	if err != nil {
		return "", fmt.Errorf("failed to get secret from Key Vault: %w", err)
	}

	if resp.Value == nil {
		return "", fmt.Errorf("secret value is nil")
	}

	return *resp.Value, nil
}
