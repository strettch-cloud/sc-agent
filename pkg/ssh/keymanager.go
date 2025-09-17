package ssh

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
)

const (
	ConsoleKeySuffix   = "-strettch_cloud_sat"
	ConsoleKeyComment  = "# Strettch Cloud Console Access Key"
	SSHDirPerm         = 0700
	AuthorizedKeysPerm = 0600
)

// ConsoleKeyData represents the structured key data from API
type ConsoleKeyData struct {
	PublicKey   string
	Principal   string
	RequesterID string
	CreatedAt   string
	ExpireAt    string
}

type KeyManager struct {
	logger             *zap.Logger
	authorizedKeysPath string
	sshDirPath         string
}

func NewKeyManager(logger *zap.Logger) (*KeyManager, error) {
	currentUser, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("failed to get current user: %w", err)
	}

	sshDir := filepath.Join(currentUser.HomeDir, ".ssh")
	return &KeyManager{
		logger:             logger,
		sshDirPath:         sshDir,
		authorizedKeysPath: filepath.Join(sshDir, "authorized_keys"),
	}, nil
}

// Install adds or updates the console access key in authorized_keys
func (km *KeyManager) Install(keyData *ConsoleKeyData) error {
	km.logger.Info("Installing console key",
		zap.String("principal", keyData.Principal),
		zap.String("created", keyData.CreatedAt),
		zap.String("expires", keyData.ExpireAt))

	if err := os.MkdirAll(km.sshDirPath, SSHDirPerm); err != nil {
		return fmt.Errorf("failed to create SSH directory: %w", err)
	}

	existingKeys, err := km.readKeys()
	if err != nil && !os.IsNotExist(err) {
		km.logger.Warn("Failed to read authorized_keys", zap.Error(err))
		existingKeys = []string{}
	}

	updatedKeys := km.updateWithConsoleKey(existingKeys, keyData)
	return km.writeKeys(updatedKeys)
}

// RemoveConsoleKeys removes all console-managed keys from authorized_keys
func (km *KeyManager) RemoveConsoleKeys() error {
	existingKeys, err := km.readKeys()
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read authorized_keys: %w", err)
	}

	userKeys := km.filterUserKeys(existingKeys)
	
	// Only write if something was removed
	if len(userKeys) < len(existingKeys) {
		removed := len(existingKeys) - len(userKeys)
		km.logger.Debug("Removed console keys", zap.Int("count", removed))
		return km.writeKeys(userKeys)
	}

	return nil
}

// filterUserKeys returns only non-console managed keys
func (km *KeyManager) filterUserKeys(keys []string) []string {
	var userKeys []string
	
	for _, key := range keys {
		trimmed := strings.TrimSpace(key)
		
		// Skip console-managed entries
		if trimmed == ConsoleKeyComment || strings.HasSuffix(trimmed, ConsoleKeySuffix) {
			continue
		}
		
		userKeys = append(userKeys, key)
	}
	
	return userKeys
}

// updateWithConsoleKey filters out old console keys and appends the new one
func (km *KeyManager) updateWithConsoleKey(keys []string, consoleKey *ConsoleKeyData) []string {
	// Start with only user keys
	updatedKeys := km.filterUserKeys(keys)
	
	km.logger.Debug("Updating authorized keys",
		zap.Int("user_keys", len(updatedKeys)),
		zap.Bool("has_console_key", consoleKey != nil))
	
	// Append new console key if provided
	if consoleKey != nil {
		formattedKey := km.formatKeyWithMetadata(consoleKey)
		updatedKeys = append(updatedKeys, ConsoleKeyComment, formattedKey)
	}
	
	return updatedKeys
}

// formatKeyWithMetadata formats the key with JSON metadata suffix
func (km *KeyManager) formatKeyWithMetadata(keyData *ConsoleKeyData) string {
	metadata := map[string]string{
		"principal":  keyData.Principal,
		"requester":  keyData.RequesterID,
		"created_at": keyData.CreatedAt,
		"expire_at":  keyData.ExpireAt,
	}
	
	metadataJSON, _ := json.Marshal(metadata)
	return fmt.Sprintf("%s %s%s", keyData.PublicKey, string(metadataJSON), ConsoleKeySuffix)
}

func (km *KeyManager) readKeys() ([]string, error) {
	file, err := os.Open(km.authorizedKeysPath)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := file.Close(); err != nil {
			// Log error but don't fail the function
			_ = err
		}
	}()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan file: %w", err)
	}

	return lines, nil
}

func (km *KeyManager) writeKeys(keys []string) error {
	tmpPath := km.authorizedKeysPath + ".tmp"
	
	// Create temp file with proper permissions
	tmpFile, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, AuthorizedKeysPerm)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}

	// Write all keys with single trailing newline
	content := strings.Join(keys, "\n")
	if _, err := fmt.Fprintf(tmpFile, "%s\n", strings.TrimSpace(content)); err != nil {
		if closeErr := tmpFile.Close(); closeErr != nil {
			// Log close error but continue with removal
			_ = closeErr // Suppress staticcheck SA9003
		}
		if removeErr := os.Remove(tmpPath); removeErr != nil {
			// Log remove error but continue with returning original error
			_ = removeErr // Suppress staticcheck SA9003
		}
		return fmt.Errorf("failed to write keys: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		if removeErr := os.Remove(tmpPath); removeErr != nil {
			// Log remove error but continue with returning original error
			_ = removeErr // Suppress staticcheck SA9003
		}
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	// Atomic replace
	if err := os.Rename(tmpPath, km.authorizedKeysPath); err != nil {
		if removeErr := os.Remove(tmpPath); removeErr != nil {
			// Log remove error but continue with returning original error
			_ = removeErr // Suppress staticcheck SA9003
		}
		return fmt.Errorf("failed to replace authorized_keys: %w", err)
	}

	return nil
}