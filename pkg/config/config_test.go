package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultConfig reads no file, so the machine running the test can't change
// the result.
func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.MetadataServiceEndpoint != "" {
		t.Errorf("MetadataServiceEndpoint = %q, want it left for Load to fill in", cfg.MetadataServiceEndpoint)
	}
	if cfg.Console.UDPKnockPort != 60124 || cfg.Console.HTTPNotifyPort != 60125 {
		t.Errorf("ports = %d/%d, want 60124/60125", cfg.Console.UDPKnockPort, cfg.Console.HTTPNotifyPort)
	}
	if cfg.KeyCleanupInterval != 120*time.Second || cfg.LogLevel != "info" {
		t.Errorf("cleanup interval %v and log level %q, want 2m0s and info", cfg.KeyCleanupInterval, cfg.LogLevel)
	}
}

func TestLoadFromEnv(t *testing.T) {
	// Create a temporary agent.yaml file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "agent.yaml")

	yamlContent := `metadata_service:
  base_url: http://test.example.com
`
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("Failed to create test config: %v", err)
	}

	// Temporarily override agentConfigPath by setting up environment
	oldConfigPath := os.Getenv("SC_AGENT_CONFIG")
	defer func() {
		if oldConfigPath != "" {
			if err := os.Setenv("SC_AGENT_CONFIG", oldConfigPath); err != nil {
				t.Logf("Failed to restore SC_AGENT_CONFIG: %v", err)
			}
		} else {
			if err := os.Unsetenv("SC_AGENT_CONFIG"); err != nil {
				t.Logf("Failed to unset SC_AGENT_CONFIG: %v", err)
			}
		}
	}()

	if err := os.Setenv("SC_AGENT_CONFIG", configPath); err != nil {
		t.Fatalf("Failed to set SC_AGENT_CONFIG: %v", err)
	}

	// Test environment variable override
	if err := os.Setenv("SC_METADATA_ENDPOINT", "http://override.example.com/metadata/v1/auth-token"); err != nil {
		t.Fatalf("Failed to set SC_METADATA_ENDPOINT: %v", err)
	}
	defer func() {
		if err := os.Unsetenv("SC_METADATA_ENDPOINT"); err != nil {
			t.Logf("Failed to unset SC_METADATA_ENDPOINT: %v", err)
		}
	}()

	if err := os.Setenv("SC_RESOURCE_ID", "override-resource-id"); err != nil {
		t.Fatalf("Failed to set SC_RESOURCE_ID: %v", err)
	}
	defer func() {
		if err := os.Unsetenv("SC_RESOURCE_ID"); err != nil {
			t.Logf("Failed to unset SC_RESOURCE_ID: %v", err)
		}
	}()

	if err := os.Setenv("SC_RESOURCE_IP", "10.0.0.1"); err != nil {
		t.Fatalf("Failed to set SC_RESOURCE_IP: %v", err)
	}
	defer func() {
		if err := os.Unsetenv("SC_RESOURCE_IP"); err != nil {
			t.Logf("Failed to unset SC_RESOURCE_IP: %v", err)
		}
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.MetadataServiceEndpoint != "http://override.example.com/metadata/v1/auth-token" {
		t.Errorf("Expected overridden metadata endpoint, got %s", cfg.MetadataServiceEndpoint)
	}

	if cfg.ResourceID != "override-resource-id" {
		t.Errorf("Expected overridden resource ID, got %s", cfg.ResourceID)
	}

	if cfg.ResourceIP != "10.0.0.1" {
		t.Errorf("Expected overridden resource IP, got %s", cfg.ResourceIP)
	}
}

func TestLoadFromFile(t *testing.T) {
	// Create a temporary agent.yaml file
	tmpDir := t.TempDir()
	agentConfigPath := filepath.Join(tmpDir, "agent.yaml")

	agentYAML := `metadata_service:
  base_url: http://192.168.1.100:9000
`
	if err := os.WriteFile(agentConfigPath, []byte(agentYAML), 0644); err != nil {
		t.Fatalf("Failed to create agent.yaml: %v", err)
	}

	// Create a test config YAML file
	testConfigPath := filepath.Join(tmpDir, "test-config.yaml")
	testConfig := `metadata_service_endpoint: http://file.example.com/metadata/v1/auth-token
resource_id: file-resource-id
resource_ip: 192.168.1.50
console:
  udp_knock_port: 12345
  http_notify_port: 12346
  max_packet_size: 2048
  signature_timeout: 60
http_timeout: 60s
log_level: debug
key_cleanup_interval: 240s
max_retries: 5
retry_interval: 10s
`
	if err := os.WriteFile(testConfigPath, []byte(testConfig), 0644); err != nil {
		t.Fatalf("Failed to create test config: %v", err)
	}

	// Set SC_AGENT_CONFIG to the agent.yaml path so DefaultConfig can read it
	if err := os.Setenv("SC_AGENT_CONFIG", agentConfigPath); err != nil {
		t.Fatalf("Failed to set SC_AGENT_CONFIG: %v", err)
	}
	defer func() {
		if err := os.Unsetenv("SC_AGENT_CONFIG"); err != nil {
			t.Logf("Failed to unset SC_AGENT_CONFIG: %v", err)
		}
	}()

	// Set resource ID and IP for test environment (dmidecode not available)
	if err := os.Setenv("SC_RESOURCE_ID", "test-resource-id"); err != nil {
		t.Fatalf("Failed to set SC_RESOURCE_ID: %v", err)
	}
	defer func() {
		if err := os.Unsetenv("SC_RESOURCE_ID"); err != nil {
			t.Logf("Failed to unset SC_RESOURCE_ID: %v", err)
		}
	}()
	if err := os.Setenv("SC_RESOURCE_IP", "192.168.1.1"); err != nil {
		t.Fatalf("Failed to set SC_RESOURCE_IP: %v", err)
	}
	defer func() {
		if err := os.Unsetenv("SC_RESOURCE_IP"); err != nil {
			t.Logf("Failed to unset SC_RESOURCE_IP: %v", err)
		}
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Load the test config file
	if _, err := cfg.loadFromFile(testConfigPath); err != nil {
		t.Fatalf("loadFromFile failed: %v", err)
	}

	if cfg.MetadataServiceEndpoint != "http://file.example.com/metadata/v1/auth-token" {
		t.Errorf("Expected metadata endpoint from file, got %s", cfg.MetadataServiceEndpoint)
	}

	if cfg.ResourceID != "file-resource-id" {
		t.Errorf("Expected resource ID from file, got %s", cfg.ResourceID)
	}

	if cfg.ResourceIP != "192.168.1.50" {
		t.Errorf("Expected resource IP from file, got %s", cfg.ResourceIP)
	}

	if cfg.Console.UDPKnockPort != 12345 {
		t.Errorf("Expected UDP knock port 12345, got %d", cfg.Console.UDPKnockPort)
	}

	if cfg.HTTPTimeout != 60*time.Second {
		t.Errorf("Expected HTTP timeout 60s, got %v", cfg.HTTPTimeout)
	}

	if cfg.LogLevel != "debug" {
		t.Errorf("Expected log level debug, got %s", cfg.LogLevel)
	}
}

func TestLoad(t *testing.T) {
	// Create a temporary agent.yaml file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "agent.yaml")

	yamlContent := `metadata_service:
  base_url: http://test.load.example.com
`
	if err := os.WriteFile(configPath, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("Failed to create test config: %v", err)
	}

	// Set environment to use the temporary config
	if err := os.Setenv("SC_AGENT_CONFIG", configPath); err != nil {
		t.Fatalf("Failed to set SC_AGENT_CONFIG: %v", err)
	}
	defer func() {
		if err := os.Unsetenv("SC_AGENT_CONFIG"); err != nil {
			t.Logf("Failed to unset SC_AGENT_CONFIG: %v", err)
		}
	}()

	// Set resource ID and IP for test environment (dmidecode not available)
	if err := os.Setenv("SC_RESOURCE_ID", "test-resource-id"); err != nil {
		t.Fatalf("Failed to set SC_RESOURCE_ID: %v", err)
	}
	defer func() {
		if err := os.Unsetenv("SC_RESOURCE_ID"); err != nil {
			t.Logf("Failed to unset SC_RESOURCE_ID: %v", err)
		}
	}()
	if err := os.Setenv("SC_RESOURCE_IP", "192.168.1.1"); err != nil {
		t.Fatalf("Failed to set SC_RESOURCE_IP: %v", err)
	}
	defer func() {
		if err := os.Unsetenv("SC_RESOURCE_IP"); err != nil {
			t.Logf("Failed to unset SC_RESOURCE_IP: %v", err)
		}
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.MetadataServiceEndpoint == "" {
		t.Error("Expected metadata service endpoint to be set")
	}

	// Verify default values are set
	if cfg.HTTPTimeout != 30*time.Second {
		t.Errorf("Expected default HTTP timeout 30s, got %v", cfg.HTTPTimeout)
	}

	if cfg.LogLevel != "info" {
		t.Errorf("Expected default log level info, got %s", cfg.LogLevel)
	}

	if cfg.Console.UDPKnockPort != 60124 {
		t.Errorf("Expected default UDP knock port 60124, got %d", cfg.Console.UDPKnockPort)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name        string
		modifyFunc  func(*Config)
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid config",
			modifyFunc: func(c *Config) {
				c.ResourceID = "test-resource"
				c.ResourceIP = "192.168.1.1"
			},
			expectError: false,
		},
		{
			name: "empty metadata endpoint",
			modifyFunc: func(c *Config) {
				c.MetadataServiceEndpoint = ""
				c.ResourceID = "test-resource"
				c.ResourceIP = "192.168.1.1"
			},
			expectError: true,
			errorMsg:    "metadata_service_endpoint cannot be empty",
		},
		{
			name: "empty resource ID",
			modifyFunc: func(c *Config) {
				c.ResourceID = ""
				c.ResourceIP = "192.168.1.1"
			},
			expectError: true,
			errorMsg:    "resource_id cannot be determined",
		},
		{
			name: "empty resource IP",
			modifyFunc: func(c *Config) {
				c.ResourceID = "test-resource"
				c.ResourceIP = ""
			},
			expectError: true,
			errorMsg:    "resource_ip cannot be determined",
		},
		{
			name: "invalid HTTP timeout",
			modifyFunc: func(c *Config) {
				c.ResourceID = "test-resource"
				c.ResourceIP = "192.168.1.1"
				c.HTTPTimeout = 0
			},
			expectError: true,
			errorMsg:    "http_timeout must be positive",
		},
		{
			name: "negative max retries",
			modifyFunc: func(c *Config) {
				c.ResourceID = "test-resource"
				c.ResourceIP = "192.168.1.1"
				c.MaxRetries = -1
			},
			expectError: true,
			errorMsg:    "max_retries cannot be negative",
		},
		{
			name: "invalid retry interval",
			modifyFunc: func(c *Config) {
				c.ResourceID = "test-resource"
				c.ResourceIP = "192.168.1.1"
				c.RetryInterval = 0
			},
			expectError: true,
			errorMsg:    "retry_interval must be positive",
		},
		{
			name: "invalid key cleanup interval",
			modifyFunc: func(c *Config) {
				c.ResourceID = "test-resource"
				c.ResourceIP = "192.168.1.1"
				c.KeyCleanupInterval = 0
			},
			expectError: true,
			errorMsg:    "key_cleanup_interval must be positive",
		},
		{
			name: "invalid HTTP notify port - too low",
			modifyFunc: func(c *Config) {
				c.ResourceID = "test-resource"
				c.ResourceIP = "192.168.1.1"
				c.Console.HTTPNotifyPort = 0
			},
			expectError: true,
			errorMsg:    "http_notify_port must be between 1 and 65535",
		},
		{
			name: "invalid HTTP notify port - too high",
			modifyFunc: func(c *Config) {
				c.ResourceID = "test-resource"
				c.ResourceIP = "192.168.1.1"
				c.Console.HTTPNotifyPort = 65536
			},
			expectError: true,
			errorMsg:    "http_notify_port must be between 1 and 65535",
		},
		{
			name: "invalid signature timeout",
			modifyFunc: func(c *Config) {
				c.ResourceID = "test-resource"
				c.ResourceIP = "192.168.1.1"
				c.Console.SignatureTimeout = 0
			},
			expectError: true,
			errorMsg:    "signature_timeout must be positive",
		},
		{
			name: "invalid log level",
			modifyFunc: func(c *Config) {
				c.ResourceID = "test-resource"
				c.ResourceIP = "192.168.1.1"
				c.LogLevel = "invalid"
			},
			expectError: true,
			errorMsg:    "invalid log_level: invalid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a config manually instead of calling DefaultConfig() which requires agent.yaml
			cfg := &Config{
				MetadataServiceEndpoint: "http://test.endpoint/metadata/v1/auth-token",
				ResourceID:              "test-resource",
				ResourceIP:              "192.168.1.1",
				Console: ConsoleConfig{
					UDPKnockPort:     60124,
					HTTPNotifyPort:   60125,
					MaxPacketSize:    1024,
					SignatureTimeout: 30,
				},
				HTTPTimeout:        30 * time.Second,
				LogLevel:           "info",
				KeyCleanupInterval: 120 * time.Second,
				MaxRetries:         3,
				RetryInterval:      5 * time.Second,
			}
			tt.modifyFunc(cfg)

			err := cfg.validate()

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error but got none")
				} else if !strings.Contains(err.Error(), tt.errorMsg) {
					t.Errorf("Expected error containing '%s', got '%s'", tt.errorMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
			}
		})
	}
}

func TestDetectResourceID(t *testing.T) {
	// This test is environment-dependent, so we just ensure it doesn't panic
	// and returns a string (could be empty if dmidecode is not available)
	resourceID := detectResourceID()

	// Should return a string (empty or valid UUID)
	if resourceID != "" {
		t.Logf("Detected resource ID: %s", resourceID)
	}
}

func TestDetectResourceIP(t *testing.T) {
	// This test is environment-dependent, so we just ensure it doesn't panic
	// and returns a string (could be empty if commands fail)
	resourceIP := detectResourceIP()

	// Should return a string (empty or valid IP)
	if resourceIP != "" {
		t.Logf("Detected resource IP: %s", resourceIP)
	}
}

func TestReadMetadataBaseURLFromYAML(t *testing.T) {
	testCases := []struct {
		name        string
		agentYAML   string
		expectedURL string
		expectError bool
		createFile  bool
	}{
		{
			name: "Valid agent.yaml with standard URL",
			agentYAML: `metadata_service:
  base_url: http://169.254.169.254
`,
			expectedURL: "http://169.254.169.254",
			expectError: false,
			createFile:  true,
		},
		{
			name: "Valid agent.yaml with custom URL",
			agentYAML: `metadata_service:
  base_url: http://172.22.10.12:8000
`,
			expectedURL: "http://172.22.10.12:8000",
			expectError: false,
			createFile:  true,
		},
		{
			name: "Valid agent.yaml with HTTPS URL",
			agentYAML: `metadata_service:
  base_url: https://metadata.example.com
`,
			expectedURL: "https://metadata.example.com",
			expectError: false,
			createFile:  true,
		},
		{
			name:        "File does not exist - return error",
			expectError: true,
			createFile:  false,
		},
		{
			name:        "Invalid YAML - return error",
			agentYAML:   "invalid: yaml: content:",
			expectError: true,
			createFile:  true,
		},
		{
			name: "Empty base_url - return error",
			agentYAML: `metadata_service:
  base_url: ""
`,
			expectError: true,
			createFile:  true,
		},
		{
			name: "Missing metadata_service section - return error",
			agentYAML: `other_config:
  value: test
`,
			expectError: true,
			createFile:  true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create a temporary directory for test files
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "agent.yaml")

			if tc.createFile {
				// Write the test YAML to the temp file
				if err := os.WriteFile(configPath, []byte(tc.agentYAML), 0644); err != nil {
					t.Fatalf("Failed to create test file: %v", err)
				}
			}
			pointAt(t, &agentConfigPath, configPath)

			result, parseErr := readMetadataBaseURL()

			if tc.expectError {
				if parseErr == nil {
					t.Errorf("Expected error but got none")
				} else {
					t.Logf("✓ Correctly returned error: %v", parseErr)
				}
			} else {
				if parseErr != nil {
					t.Errorf("Unexpected error: %v", parseErr)
				} else if result != tc.expectedURL {
					t.Errorf("Expected URL %s, got %s", tc.expectedURL, result)
				} else {
					t.Logf("✓ Correctly returned: %s", result)
				}
			}
		})
	}
}

func TestAgentYAMLStructure(t *testing.T) {
	// Test that the AgentConfig structure correctly unmarshals YAML
	yamlContent := `metadata_service:
  base_url: http://test.example.com:8080
`

	var config AgentConfig
	if err := yaml.Unmarshal([]byte(yamlContent), &config); err != nil {
		t.Fatalf("Failed to unmarshal agent.yaml structure: %v", err)
	}

	if config.MetadataService.BaseURL != "http://test.example.com:8080" {
		t.Errorf("Expected base_url to be 'http://test.example.com:8080', got '%s'", config.MetadataService.BaseURL)
	}

	t.Logf("✓ AgentConfig structure correctly parses YAML")
}
