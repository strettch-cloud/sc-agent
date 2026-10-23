package pipeline

import (
	"testing"
	"time"

	"github.com/strettch-cloud/sc-agent/pkg/config"
	"go.uber.org/zap"
)

func TestNewProcessor(t *testing.T) {
	cfg := &config.Config{
		MetadataServiceEndpoint: "http://metadata.example.com",
		ResourceID:              "test-resource-123",
		HTTPTimeout:             30 * time.Second,
		KeyCleanupInterval:      120 * time.Second,
		HeartbeatInterval:       5 * time.Minute,
	}

	logger := zap.NewNop()

	processor, err := NewProcessor(cfg, logger)
	if err != nil {
		t.Fatalf("Failed to create processor: %v", err)
	}

	if processor == nil {
		t.Fatal("Expected non-nil processor")
	}

	if processor.config != cfg {
		t.Error("Config not properly set")
	}

	if processor.logger != logger {
		t.Error("Logger not properly set")
	}

	if processor.metadataClient == nil {
		t.Error("Metadata client should be initialized")
	}

	if processor.cloudAPIClient == nil {
		t.Error("Cloud API client should be initialized")
	}

	if processor.keyManager == nil {
		t.Error("Key manager should be initialized")
	}

	if processor.udpListener == nil {
		t.Error("UDP listener should be initialized")
	}

	if processor.cleanupTicker == nil {
		t.Error("Cleanup ticker should be initialized")
	}
}

func TestConstants(t *testing.T) {
	if knockProcessTimeout != 30*time.Second {
		t.Errorf("Expected knockProcessTimeout to be 30s, got %v", knockProcessTimeout)
	}
}

func TestProcessorStartStop(t *testing.T) {
	cfg := &config.Config{
		MetadataServiceEndpoint: "http://metadata.example.com",
		ResourceID:              "test-resource-123",
		HTTPTimeout:             30 * time.Second,
		KeyCleanupInterval:      120 * time.Second,
		HeartbeatInterval:       5 * time.Minute,
		Console: config.ConsoleConfig{
			UDPKnockPort:     0, // Let OS choose port for testing
			MaxPacketSize:    1024,
			SignatureTimeout: 30,
		},
	}

	logger := zap.NewNop()

	processor, err := NewProcessor(cfg, logger)
	if err != nil {
		t.Fatalf("Failed to create processor: %v", err)
	}

	// Test that Stop doesn't panic when called before Start
	processor.Stop()
}

func TestProcessorConfiguration(t *testing.T) {
	tests := []struct {
		name        string
		config      *config.Config
		expectError bool
	}{
		{
			name: "valid configuration",
			config: &config.Config{
				MetadataServiceEndpoint: "http://metadata.example.com",
				ResourceID:              "test-resource-123",
				HTTPTimeout:             30 * time.Second,
				KeyCleanupInterval:      120 * time.Second,
				HeartbeatInterval:       5 * time.Minute,
				Console: config.ConsoleConfig{
					UDPKnockPort:     60124,
					MaxPacketSize:    1024,
					SignatureTimeout: 30,
				},
			},
			expectError: false,
		},
		{
			name: "invalid configuration - missing resource ID",
			config: &config.Config{
				MetadataServiceEndpoint: "http://metadata.example.com",
				ResourceID:              "",
				HTTPTimeout:             30 * time.Second,
				KeyCleanupInterval:      120 * time.Second,
				HeartbeatInterval:       5 * time.Minute,
			},
			expectError: false, // NewProcessor doesn't validate config
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := zap.NewNop()

			processor, err := NewProcessor(tt.config, logger)

			if tt.expectError {
				if err == nil {
					t.Error("Expected error but got none")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}

				if processor == nil {
					t.Error("Expected processor but got nil")
				}
			}
		})
	}
}
