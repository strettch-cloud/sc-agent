package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	
	if cfg.MetadataServiceEndpoint != "http://169.254.169.254/metadata/v1/auth-token" {
		t.Errorf("Expected default metadata endpoint, got %s", cfg.MetadataServiceEndpoint)
	}
	
	if cfg.Console.UDPKnockPort != 60124 {
		t.Errorf("Expected UDP knock port 60124, got %d", cfg.Console.UDPKnockPort)
	}
	
	if cfg.Console.HTTPNotifyPort != 60125 {
		t.Errorf("Expected HTTP notify port 60125, got %d", cfg.Console.HTTPNotifyPort)
	}
	
	if cfg.HTTPTimeout != 30*time.Second {
		t.Errorf("Expected HTTP timeout 30s, got %v", cfg.HTTPTimeout)
	}
	
	if cfg.LogLevel != "info" {
		t.Errorf("Expected log level 'info', got %s", cfg.LogLevel)
	}
	
	if cfg.MaxRetries != 3 {
		t.Errorf("Expected max retries 3, got %d", cfg.MaxRetries)
	}
}

func TestLoadFromEnv(t *testing.T) {
	tests := []struct {
		name     string
		envVars  map[string]string
		expected func(*Config) bool
	}{
		{
			name: "metadata endpoint override",
			envVars: map[string]string{
				"SC_METADATA_ENDPOINT": "http://custom.endpoint",
			},
			expected: func(c *Config) bool {
				return c.MetadataServiceEndpoint == "http://custom.endpoint"
			},
		},
		{
			name: "resource ID override",
			envVars: map[string]string{
				"SC_RESOURCE_ID": "test-resource-123",
			},
			expected: func(c *Config) bool {
				return c.ResourceID == "test-resource-123"
			},
		},
		{
			name: "resource IP override",
			envVars: map[string]string{
				"SC_RESOURCE_IP": "192.168.1.100",
			},
			expected: func(c *Config) bool {
				return c.ResourceIP == "192.168.1.100"
			},
		},
		{
			name: "log level override",
			envVars: map[string]string{
				"SC_LOG_LEVEL": "debug",
			},
			expected: func(c *Config) bool {
				return c.LogLevel == "debug"
			},
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set environment variables
			for key, value := range tt.envVars {
				if err := os.Setenv(key, value); err != nil {
					t.Fatalf("Failed to set env var %s: %v", key, err)
				}
				defer func(k string) {
					if err := os.Unsetenv(k); err != nil {
						t.Logf("Failed to unset env var %s: %v", k, err)
					}
				}(key)
			}
			
			cfg := DefaultConfig()
			cfg.loadFromEnv()
			
			if !tt.expected(cfg) {
				t.Errorf("Environment variable override failed for %s", tt.name)
			}
		})
	}
}

func TestLoadFromFile(t *testing.T) {
	// Create a temporary config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test-config.yaml")
	
	configContent := `
metadata_service_endpoint: "http://test.endpoint"
resource_id: "file-resource-123"
resource_ip: "10.0.0.1"
http_timeout: 45s
log_level: "debug"
max_retries: 5
retry_interval: 10s
key_cleanup_interval: 180s
console:
  udp_knock_port: 60200
  http_notify_port: 60201
  max_packet_size: 2048
  signature_timeout: 60
`
	
	err := os.WriteFile(configPath, []byte(configContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create test config file: %v", err)
	}
	
	cfg := DefaultConfig()
	err = cfg.loadFromFile(configPath)
	if err != nil {
		t.Fatalf("Failed to load config from file: %v", err)
	}
	
	if cfg.MetadataServiceEndpoint != "http://test.endpoint" {
		t.Errorf("Expected metadata endpoint from file, got %s", cfg.MetadataServiceEndpoint)
	}
	
	if cfg.ResourceID != "file-resource-123" {
		t.Errorf("Expected resource ID from file, got %s", cfg.ResourceID)
	}
	
	if cfg.HTTPTimeout != 45*time.Second {
		t.Errorf("Expected HTTP timeout 45s from file, got %v", cfg.HTTPTimeout)
	}
	
	if cfg.Console.UDPKnockPort != 60200 {
		t.Errorf("Expected UDP knock port 60200 from file, got %d", cfg.Console.UDPKnockPort)
	}
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name        string
		configFile  string
		envVars     map[string]string
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid config with env overrides",
			envVars: map[string]string{
				"SC_RESOURCE_ID": "env-resource-123",
				"SC_RESOURCE_IP": "192.168.1.1",
			},
			expectError: false,
		},
		{
			name: "missing resource ID",
			envVars: map[string]string{
				"SC_RESOURCE_IP": "192.168.1.1",
			},
			expectError: true,
			errorMsg:    "resource_id cannot be determined",
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear existing env vars
			if err := os.Unsetenv("SC_AGENT_CONFIG"); err != nil {
				t.Logf("Failed to unset SC_AGENT_CONFIG: %v", err)
			}
			for key := range tt.envVars {
				if err := os.Unsetenv(key); err != nil {
					t.Logf("Failed to unset env var %s: %v", key, err)
				}
			}
			
			// Set test env vars
			for key, value := range tt.envVars {
				if err := os.Setenv(key, value); err != nil {
					t.Fatalf("Failed to set env var %s: %v", key, err)
				}
				defer func(k string) {
					if err := os.Unsetenv(k); err != nil {
						t.Logf("Failed to unset env var %s: %v", k, err)
					}
				}(key)
			}
			
			cfg, err := Load()
			
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
				if cfg == nil {
					t.Errorf("Expected config but got nil")
				}
			}
		})
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
			cfg := DefaultConfig()
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