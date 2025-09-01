package ssh

import (
	"bufio"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"
)

const (
	SATKeySuffix       = "-strettch_cloud_sat"
	SSHDirPerm         = 0700
	AuthorizedKeysPerm = 0600
)

type KeyEntry struct {
	KeyType     string
	KeyData     string
	Comment     string
	Principal   string
	RequesterID string
	CreatedAt   time.Time
	ExpiryTime  time.Time
	IsSATKey    bool
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

	sshDirPath := filepath.Join(currentUser.HomeDir, ".ssh")
	authorizedKeysPath := filepath.Join(sshDirPath, "authorized_keys")

	return &KeyManager{
		logger:             logger,
		authorizedKeysPath: authorizedKeysPath,
		sshDirPath:         sshDirPath,
	}, nil
}

func (km *KeyManager) InstallSATKey(satKey string) error {
	keyEntry, err := km.parseSATKey(satKey)
	if err != nil {
		return fmt.Errorf("failed to parse SAT key: %w", err)
	}

	km.logger.Info("Installing SAT key",
		zap.String("principal", keyEntry.Principal),
		zap.String("requester", keyEntry.RequesterID),
		zap.Time("created", keyEntry.CreatedAt),
		zap.Time("expires", keyEntry.ExpiryTime))

	if err := os.MkdirAll(km.sshDirPath, SSHDirPerm); err != nil {
		return fmt.Errorf("failed to create SSH directory: %w", err)
	}

	// Remove existing SAT keys
	if err := km.cleanupExpiredKeys(true); err != nil {
		km.logger.Warn("Failed to cleanup existing keys", zap.Error(err))
	}

	existingKeys, err := km.readAuthorizedKeys()
	if err != nil && !os.IsNotExist(err) {
		km.logger.Warn("Failed to read authorized_keys", zap.Error(err))
		existingKeys = []string{}
	}

	allKeys := append(existingKeys, satKey)

	if err := km.writeAuthorizedKeys(allKeys); err != nil {
		return fmt.Errorf("failed to write authorized_keys: %w", err)
	}

	return nil
}

func (km *KeyManager) CleanupExpiredKeys() error {
	return km.cleanupExpiredKeys(false)
}

func (km *KeyManager) cleanupExpiredKeys(removeAll bool) error {
	allKeys, err := km.readAuthorizedKeys()
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read authorized_keys: %w", err)
	}

	var validKeys []string
	var removedCount int
	now := time.Now()

	for _, keyLine := range allKeys {
		keyLine = strings.TrimSpace(keyLine)
		if keyLine == "" || strings.HasPrefix(keyLine, "#") {
			validKeys = append(validKeys, keyLine)
			continue
		}

		keyEntry, err := km.parseSATKey(keyLine)
		if err != nil {
			// Not a SAT key - keep it
			validKeys = append(validKeys, keyLine)
			continue
		}

		if removeAll || now.After(keyEntry.ExpiryTime) {
			removedCount++
			continue
		}

		validKeys = append(validKeys, keyLine)
	}

	if removedCount > 0 {
		km.logger.Debug("Removed SAT keys", zap.Int("count", removedCount))
		return km.writeAuthorizedKeys(validKeys)
	}

	return nil
}

func (km *KeyManager) GetActiveSATKeys() ([]KeyEntry, error) {
	allKeys, err := km.readAuthorizedKeys()
	if err != nil {
		if os.IsNotExist(err) {
			return []KeyEntry{}, nil
		}
		return nil, fmt.Errorf("failed to read authorized_keys: %w", err)
	}

	var satKeys []KeyEntry
	now := time.Now()

	for _, keyLine := range allKeys {
		keyLine = strings.TrimSpace(keyLine)
		if keyLine == "" || strings.HasPrefix(keyLine, "#") {
			continue
		}

		keyEntry, err := km.parseSATKey(keyLine)
		if err != nil {
			continue
		}

		if now.Before(keyEntry.ExpiryTime) {
			satKeys = append(satKeys, *keyEntry)
		}
	}

	sort.Slice(satKeys, func(i, j int) bool {
		return satKeys[i].ExpiryTime.Before(satKeys[j].ExpiryTime)
	})

	return satKeys, nil
}

func (km *KeyManager) parseSATKey(keyLine string) (*KeyEntry, error) {
	keyLine = strings.TrimSpace(keyLine)
	if !strings.HasSuffix(keyLine, SATKeySuffix) {
		return nil, fmt.Errorf("not a SAT key")
	}

	parts := strings.Fields(keyLine)
	if len(parts) < 3 {
		return nil, fmt.Errorf("invalid key format")
	}

	keyType := parts[0]
	keyData := parts[1]
	comment := strings.Join(parts[2:], " ")

	// Extract metadata: {"principal":"root","requester_id":"email","created_at":"timestamp","expire_at":"timestamp"}-strettch_cloud_sat
	jsonPart := strings.TrimSuffix(comment, SATKeySuffix)
	
	principalRegex := regexp.MustCompile(`"principal":"([^"]+)"`)
	requesterRegex := regexp.MustCompile(`"requester_id":"([^"]+)"`)
	createdRegex := regexp.MustCompile(`"created_at":"([^"]+)"`)
	expireRegex := regexp.MustCompile(`"expire_at":"([^"]+)"`)

	principalMatch := principalRegex.FindStringSubmatch(jsonPart)
	requesterMatch := requesterRegex.FindStringSubmatch(jsonPart)
	createdMatch := createdRegex.FindStringSubmatch(jsonPart)
	expireMatch := expireRegex.FindStringSubmatch(jsonPart)

	if len(principalMatch) < 2 || len(requesterMatch) < 2 || len(createdMatch) < 2 || len(expireMatch) < 2 {
		return nil, fmt.Errorf("failed to parse SAT key metadata")
	}

	createdAt, err := time.Parse(time.RFC3339, createdMatch[1])
	if err != nil {
		return nil, fmt.Errorf("failed to parse creation time: %w", err)
	}

	expiryTime, err := time.Parse(time.RFC3339, expireMatch[1])
	if err != nil {
		return nil, fmt.Errorf("failed to parse expiry time: %w", err)
	}

	return &KeyEntry{
		KeyType:     keyType,
		KeyData:     keyData,
		Comment:     comment,
		Principal:   principalMatch[1],
		RequesterID: requesterMatch[1],
		CreatedAt:   createdAt,
		ExpiryTime:  expiryTime,
		IsSATKey:    true,
	}, nil
}

func (km *KeyManager) readAuthorizedKeys() ([]string, error) {
	file, err := os.Open(km.authorizedKeysPath)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			km.logger.Warn("Failed to close authorized_keys file", zap.Error(closeErr))
		}
	}()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	return lines, nil
}

func (km *KeyManager) writeAuthorizedKeys(keys []string) error {
	tmpPath := km.authorizedKeysPath + ".tmp"
	tmpFile, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, AuthorizedKeysPerm)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}

	for _, key := range keys {
		if _, err := tmpFile.WriteString(key + "\n"); err != nil {
			if closeErr := tmpFile.Close(); closeErr != nil {
				km.logger.Warn("Failed to close temp file during cleanup", zap.Error(closeErr))
			}
			if removeErr := os.Remove(tmpPath); removeErr != nil {
				km.logger.Warn("Failed to remove temp file during cleanup", zap.Error(removeErr))
			}
			return fmt.Errorf("failed to write key: %w", err)
		}
	}

	if err := tmpFile.Close(); err != nil {
		if removeErr := os.Remove(tmpPath); removeErr != nil {
			km.logger.Warn("Failed to remove temp file after close error", zap.Error(removeErr))
		}
		return fmt.Errorf("failed to close temp file: %w", err)
	}

	// Atomic replace
	if err := os.Rename(tmpPath, km.authorizedKeysPath); err != nil {
		if removeErr := os.Remove(tmpPath); removeErr != nil {
			km.logger.Warn("Failed to remove temp file after rename error", zap.Error(removeErr))
		}
		return fmt.Errorf("failed to replace authorized_keys: %w", err)
	}

	return nil
}