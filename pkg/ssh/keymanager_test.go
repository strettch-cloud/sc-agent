package ssh

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestNewKeyManager(t *testing.T) {
	logger := zap.NewNop()
	
	km, err := NewKeyManager(logger)
	if err != nil {
		t.Fatalf("Failed to create key manager: %v", err)
	}
	
	if km == nil {
		t.Fatal("Expected non-nil key manager")
	}
	
	if km.logger != logger {
		t.Error("Logger not properly set")
	}
	
	if km.authorizedKeysPath == "" {
		t.Error("Authorized keys path should not be empty")
	}
	
	if km.sshDirPath == "" {
		t.Error("SSH directory path should not be empty")
	}
	
	// Check that paths are correctly constructed
	if !strings.HasSuffix(km.authorizedKeysPath, "authorized_keys") {
		t.Error("Authorized keys path should end with 'authorized_keys'")
	}
	
	if !strings.HasSuffix(km.sshDirPath, ".ssh") {
		t.Error("SSH directory path should end with '.ssh'")
	}
}

func TestFormatKeyWithMetadata(t *testing.T) {
	logger := zap.NewNop()
	km, err := NewKeyManager(logger)
	if err != nil {
		t.Fatalf("Failed to create key manager: %v", err)
	}
	
	keyData := &ConsoleKeyData{
		PublicKey:   "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI...",
		Principal:   "root",
		RequesterID: "user@example.com",
		CreatedAt:   "2023-01-01T10:00:00Z",
		ExpireAt:    "2023-01-02T10:00:00Z",
	}
	
	result := km.formatKeyWithMetadata(keyData)
	
	// Check that it includes the public key
	if !strings.Contains(result, keyData.PublicKey) {
		t.Error("Formatted key should contain the public key")
	}
	
	// Check that it ends with the console key suffix
	if !strings.HasSuffix(result, ConsoleKeySuffix) {
		t.Error("Formatted key should end with console key suffix")
	}
	
	// Check that it contains JSON metadata
	// Format is: "ssh-type key-data {json}suffix"
	parts := strings.Split(result, " ")
	if len(parts) != 3 {
		t.Errorf("Formatted key should have exactly 3 parts (ssh-type, key-data, json+suffix), got %d", len(parts))
		return
	}
	
	// Extract the JSON+suffix part (last part)
	jsonWithSuffix := parts[2]
	if !strings.HasSuffix(jsonWithSuffix, ConsoleKeySuffix) {
		t.Error("Should end with console key suffix")
		return
	}
	
	// Extract just the JSON part
	jsonPart := strings.TrimSuffix(jsonWithSuffix, ConsoleKeySuffix)
	if !strings.HasPrefix(jsonPart, "{") || !strings.HasSuffix(jsonPart, "}") {
		t.Error("JSON part should be valid JSON")
		return
	}
	
	// Parse JSON to verify structure
	var metadata map[string]string
	err = json.Unmarshal([]byte(jsonPart), &metadata)
	if err != nil {
		t.Errorf("Failed to parse JSON metadata: %v", err)
		return
	}
	
	expectedFields := map[string]string{
		"principal":  keyData.Principal,
		"requester":  keyData.RequesterID,
		"created_at": keyData.CreatedAt,
		"expire_at":  keyData.ExpireAt,
	}
	
	for field, expectedValue := range expectedFields {
		if metadata[field] != expectedValue {
			t.Errorf("Expected %s to be %s, got %s", field, expectedValue, metadata[field])
		}
	}
}

func TestFilterUserKeys(t *testing.T) {
	logger := zap.NewNop()
	km, err := NewKeyManager(logger)
	if err != nil {
		t.Fatalf("Failed to create key manager: %v", err)
	}
	
	keys := []string{
		"ssh-rsa AAAAB3NzaC1yc2EAAAAD... user@host",
		ConsoleKeyComment,
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI... {\"principal\":\"root\"}" + ConsoleKeySuffix,
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI... another-user@host",
		"   ", // Empty line
		"# Another comment",
		"ssh-rsa AAAAB3NzaC1yc2EAAAAD... {\"principal\":\"admin\"}" + ConsoleKeySuffix,
	}
	
	result := km.filterUserKeys(keys)
	
	expected := []string{
		"ssh-rsa AAAAB3NzaC1yc2EAAAAD... user@host",
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI... another-user@host",
		"   ",
		"# Another comment",
	}
	
	if len(result) != len(expected) {
		t.Errorf("Expected %d keys, got %d", len(expected), len(result))
	}
	
	for i, expectedKey := range expected {
		if i >= len(result) || result[i] != expectedKey {
			t.Errorf("Expected key %d to be '%s', got '%s'", i, expectedKey, result[i])
		}
	}
}

func TestUpdateWithConsoleKey(t *testing.T) {
	logger := zap.NewNop()
	km, err := NewKeyManager(logger)
	if err != nil {
		t.Fatalf("Failed to create key manager: %v", err)
	}
	
	existingKeys := []string{
		"ssh-rsa AAAAB3NzaC1yc2EAAAAD... user@host",
		ConsoleKeyComment,
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI... {\"old\":\"key\"}" + ConsoleKeySuffix,
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI... another-user@host",
	}
	
	newKey := &ConsoleKeyData{
		PublicKey:   "ssh-ed25519 NEW_KEY_DATA",
		Principal:   "root",
		RequesterID: "user@example.com",
		CreatedAt:   "2023-01-01T10:00:00Z",
		ExpireAt:    "2023-01-02T10:00:00Z",
	}
	
	result := km.updateWithConsoleKey(existingKeys, newKey)
	
	// Should have user keys + comment + new console key
	expectedCount := 2 + 1 + 1 // 2 user keys + comment + new console key
	if len(result) != expectedCount {
		t.Errorf("Expected %d keys, got %d", expectedCount, len(result))
	}
	
	// Check that user keys are preserved
	if result[0] != "ssh-rsa AAAAB3NzaC1yc2EAAAAD... user@host" {
		t.Error("First user key not preserved")
	}
	
	if result[1] != "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI... another-user@host" {
		t.Error("Second user key not preserved")
	}
	
	// Check that console comment is added
	if result[2] != ConsoleKeyComment {
		t.Error("Console key comment not added")
	}
	
	// Check that new console key is formatted correctly
	if !strings.Contains(result[3], "ssh-ed25519 NEW_KEY_DATA") {
		t.Error("New console key not properly formatted")
	}
	
	if !strings.HasSuffix(result[3], ConsoleKeySuffix) {
		t.Error("New console key should have suffix")
	}
}

func TestInstallAndRemove(t *testing.T) {
	// Create a temporary directory for testing
	tempDir := t.TempDir()
	
	// Create a mock key manager with custom paths
	logger := zap.NewNop()
	km := &KeyManager{
		logger:             logger,
		sshDirPath:         tempDir,
		authorizedKeysPath: filepath.Join(tempDir, "authorized_keys"),
	}
	
	// Create initial authorized_keys file with some user keys
	initialKeys := []string{
		"ssh-rsa AAAAB3NzaC1yc2EAAAAD... user@host",
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI... another-user@host",
	}
	
	err := os.WriteFile(km.authorizedKeysPath, []byte(strings.Join(initialKeys, "\n")+"\n"), 0600)
	if err != nil {
		t.Fatalf("Failed to create initial authorized_keys: %v", err)
	}
	
	// Test installing a console key
	keyData := &ConsoleKeyData{
		PublicKey:   "ssh-ed25519 CONSOLE_KEY_DATA",
		Principal:   "root",
		RequesterID: "admin@example.com",
		CreatedAt:   "2023-01-01T10:00:00Z",
		ExpireAt:    "2023-01-02T10:00:00Z",
	}
	
	err = km.Install(keyData)
	if err != nil {
		t.Fatalf("Failed to install console key: %v", err)
	}
	
	// Read the file and verify contents
	content, err := os.ReadFile(km.authorizedKeysPath)
	if err != nil {
		t.Fatalf("Failed to read authorized_keys after install: %v", err)
	}
	
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	
	// Should have user keys + comment + console key
	expectedLineCount := 2 + 1 + 1
	if len(lines) != expectedLineCount {
		t.Errorf("Expected %d lines, got %d", expectedLineCount, len(lines))
	}
	
	// Verify user keys are preserved
	if lines[0] != initialKeys[0] {
		t.Error("First user key not preserved")
	}
	
	if lines[1] != initialKeys[1] {
		t.Error("Second user key not preserved")
	}
	
	// Verify console key components
	if lines[2] != ConsoleKeyComment {
		t.Error("Console key comment not found")
	}
	
	if !strings.Contains(lines[3], "ssh-ed25519 CONSOLE_KEY_DATA") {
		t.Error("Console key not found")
	}
	
	// Test removing console keys
	err = km.RemoveConsoleKeys()
	if err != nil {
		t.Fatalf("Failed to remove console keys: %v", err)
	}
	
	// Read the file and verify console keys are removed
	content, err = os.ReadFile(km.authorizedKeysPath)
	if err != nil {
		t.Fatalf("Failed to read authorized_keys after removal: %v", err)
	}
	
	lines = strings.Split(strings.TrimSpace(string(content)), "\n")
	
	// Should only have user keys
	if len(lines) != 2 {
		t.Errorf("Expected 2 lines after removal, got %d", len(lines))
	}
	
	if lines[0] != initialKeys[0] || lines[1] != initialKeys[1] {
		t.Error("User keys not properly preserved after console key removal")
	}
}

func TestReadWriteKeys(t *testing.T) {
	tempDir := t.TempDir()
	
	logger := zap.NewNop()
	km := &KeyManager{
		logger:             logger,
		sshDirPath:         tempDir,
		authorizedKeysPath: filepath.Join(tempDir, "authorized_keys"),
	}
	
	// Test writing keys
	keys := []string{
		"ssh-rsa AAAAB3NzaC1yc2EAAAAD... user@host",
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI... another-user@host",
		"# A comment",
	}
	
	err := km.writeKeys(keys)
	if err != nil {
		t.Fatalf("Failed to write keys: %v", err)
	}
	
	// Verify file exists and has correct permissions
	info, err := os.Stat(km.authorizedKeysPath)
	if err != nil {
		t.Fatalf("File not created: %v", err)
	}
	
	if info.Mode().Perm() != AuthorizedKeysPerm {
		t.Errorf("Expected permissions %o, got %o", AuthorizedKeysPerm, info.Mode().Perm())
	}
	
	// Test reading keys
	readKeys, err := km.readKeys()
	if err != nil {
		t.Fatalf("Failed to read keys: %v", err)
	}
	
	if len(readKeys) != len(keys) {
		t.Errorf("Expected %d keys, got %d", len(keys), len(readKeys))
	}
	
	for i, expectedKey := range keys {
		if readKeys[i] != expectedKey {
			t.Errorf("Expected key %d to be '%s', got '%s'", i, expectedKey, readKeys[i])
		}
	}
}

func TestReadKeysNonexistent(t *testing.T) {
	tempDir := t.TempDir()
	
	logger := zap.NewNop()
	km := &KeyManager{
		logger:             logger,
		sshDirPath:         tempDir,
		authorizedKeysPath: filepath.Join(tempDir, "nonexistent"),
	}
	
	_, err := km.readKeys()
	if err == nil {
		t.Error("Expected error when reading nonexistent file")
	}
	
	if !os.IsNotExist(err) {
		t.Errorf("Expected 'file not found' error, got: %v", err)
	}
}

func TestRemoveConsoleKeysNonexistent(t *testing.T) {
	tempDir := t.TempDir()
	
	logger := zap.NewNop()
	km := &KeyManager{
		logger:             logger,
		sshDirPath:         tempDir,
		authorizedKeysPath: filepath.Join(tempDir, "nonexistent"),
	}
	
	err := km.RemoveConsoleKeys()
	if err != nil {
		t.Errorf("Should not error when removing keys from nonexistent file: %v", err)
	}
}

func TestConstants(t *testing.T) {
	if ConsoleKeySuffix != "-strettch_cloud_sat" {
		t.Errorf("Expected console key suffix '-strettch_cloud_sat', got '%s'", ConsoleKeySuffix)
	}
	
	if ConsoleKeyComment != "# Strettch Cloud Console Access Key" {
		t.Errorf("Expected console key comment '# Strettch Cloud Console Access Key', got '%s'", ConsoleKeyComment)
	}
	
	if SSHDirPerm != 0700 {
		t.Errorf("Expected SSH dir permissions 0700, got %o", SSHDirPerm)
	}
	
	if AuthorizedKeysPerm != 0600 {
		t.Errorf("Expected authorized keys permissions 0600, got %o", AuthorizedKeysPerm)
	}
}

func TestConsoleKeyData(t *testing.T) {
	keyData := ConsoleKeyData{
		PublicKey:   "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI...",
		Principal:   "root",
		RequesterID: "user@example.com",
		CreatedAt:   "2023-01-01T10:00:00Z",
		ExpireAt:    "2023-01-02T10:00:00Z",
	}
	
	if keyData.PublicKey == "" {
		t.Error("PublicKey should not be empty")
	}
	
	if keyData.Principal != "root" {
		t.Errorf("Expected principal 'root', got '%s'", keyData.Principal)
	}
	
	if keyData.RequesterID == "" {
		t.Error("RequesterID should not be empty")
	}
	
	if keyData.CreatedAt == "" {
		t.Error("CreatedAt should not be empty")
	}
	
	if keyData.ExpireAt == "" {
		t.Error("ExpireAt should not be empty")
	}
}

// Test for atomic file replacement
func TestAtomicWrite(t *testing.T) {
	tempDir := t.TempDir()
	
	logger := zap.NewNop()
	km := &KeyManager{
		logger:             logger,
		sshDirPath:         tempDir,
		authorizedKeysPath: filepath.Join(tempDir, "authorized_keys"),
	}
	
	// Create initial file
	initialContent := "initial content\n"
	err := os.WriteFile(km.authorizedKeysPath, []byte(initialContent), 0600)
	if err != nil {
		t.Fatalf("Failed to create initial file: %v", err)
	}
	
	// Write new keys
	keys := []string{"new", "keys"}
	err = km.writeKeys(keys)
	if err != nil {
		t.Fatalf("Failed to write keys: %v", err)
	}
	
	// Verify content was replaced
	content, err := os.ReadFile(km.authorizedKeysPath)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}
	
	expectedContent := "new\nkeys\n"
	if string(content) != expectedContent {
		t.Errorf("Expected content '%s', got '%s'", expectedContent, string(content))
	}
	
	// Verify no temporary files are left behind
	files, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatalf("Failed to read directory: %v", err)
	}
	
	for _, file := range files {
		if strings.Contains(file.Name(), ".tmp") {
			t.Error("Temporary file not cleaned up")
		}
	}
}