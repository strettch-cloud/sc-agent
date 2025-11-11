package config

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	// Service endpoints
	MetadataServiceEndpoint string `yaml:"metadata_service_endpoint"`
	
	// Resource identification
	ResourceID string `yaml:"resource_id"`
	ResourceIP string `yaml:"resource_ip"`
	
	Console ConsoleConfig `yaml:"console"`
	
	// HTTP settings
	HTTPTimeout time.Duration `yaml:"http_timeout"`
	
	// Operational settings
	LogLevel           string        `yaml:"log_level"`
	KeyCleanupInterval time.Duration `yaml:"key_cleanup_interval"`
	MaxRetries         int           `yaml:"max_retries"`
	RetryInterval      time.Duration `yaml:"retry_interval"`
	AgentVersion       string        `yaml:"agent_version"`
}

type ConsoleConfig struct {
	UDPKnockPort     int `yaml:"udp_knock_port"`
	HTTPNotifyPort   int `yaml:"http_notify_port"`
	MaxPacketSize    int `yaml:"max_packet_size"`
	SignatureTimeout int `yaml:"signature_timeout"`
}

// AgentConfig represents the structure of agent.yaml
type AgentConfig struct {
	MetadataService struct {
		BaseURL string `yaml:"base_url"`
	} `yaml:"metadata_service"`
}

const agentConfigPath = "/etc/strettchcloud/config/agent.yaml"

// readMetadataBaseURL reads the agent configuration and returns the metadata service base URL.
// It respects SC_AGENT_CONFIG environment variable for testing purposes.
func readMetadataBaseURL() (string, error) {
	configPath := agentConfigPath
	if envPath := os.Getenv("SC_AGENT_CONFIG"); envPath != "" {
		configPath = envPath
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return "", fmt.Errorf("read config: %w", err)
	}

	var config AgentConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return "", fmt.Errorf("parse config: %w", err)
	}

	if config.MetadataService.BaseURL == "" {
		return "", errors.New("metadata base_url not configured")
	}

	return config.MetadataService.BaseURL, nil
}

func DefaultConfig() (*Config, error) {
	metadataBaseURL, err := readMetadataBaseURL()
	if err != nil {
		return nil, fmt.Errorf("failed to read metadata service configuration: %w", err)
	}
	metadataEndpoint := metadataBaseURL + "/metadata/v1/auth-token"

	return &Config{
		MetadataServiceEndpoint: metadataEndpoint,
		ResourceID:              detectResourceID(),
		ResourceIP:              detectResourceIP(),
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
		AgentVersion:       detectAgentVersion(),
	}, nil
}

func Load() (*Config, error) {
	cfg, err := DefaultConfig()
	if err != nil {
		return nil, err
	}

	// Load from config file if specified
	if configPath := os.Getenv("SC_AGENT_CONFIG"); configPath != "" {
		if err := cfg.loadFromFile(configPath); err != nil {
			return nil, fmt.Errorf("failed to load config file %s: %w", configPath, err)
		}
	}

	// Override with environment variables
	cfg.loadFromEnv()

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("configuration validation failed: %w", err)
	}

	return cfg, nil
}

func detectResourceID() string {
	// Preferred method: DMI information
	if id, err := os.ReadFile("/sys/class/dmi/id/product_uuid"); err == nil {
		uuid := strings.TrimSpace(string(id))
		if isValidUUID(uuid) {
			return uuid
		}
	}

	// Fallback for non-DMI systems or permission errors
	if id, err := os.ReadFile("/etc/machine-id"); err == nil {
		return strings.TrimSpace(string(id))
	}

	// Legacy fallback (e.g., inside containers without /sys)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "dmidecode", "-s", "system-uuid")
	if output, err := cmd.Output(); err == nil {
		uuid := strings.TrimSpace(string(output))
		if isValidUUID(uuid) {
			return uuid
		}
	}

	return ""
}

func isValidUUID(uuid string) bool {
	return uuid != "" &&
		!strings.EqualFold(uuid, "Not Settable") &&
		!strings.EqualFold(uuid, "Not Specified") &&
		!strings.HasPrefix(uuid, "00000000-0000-0000")
}

func detectResourceIP() string {
	// Use net.Dial to find the preferred outbound IP
	if conn, err := net.Dial("udp", "8.8.8.8:80"); err == nil {
		defer conn.Close()
		if localAddr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
			return localAddr.IP.String()
		}
	}

	// Fallback for environments without internet access
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}

	for _, address := range addrs {
		if ipnet, ok := address.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}

	return ""
}

func detectAgentVersion() string {
	cmd := exec.Command("dpkg-query", "-W", "-f=${Version}", "sc-console-agent")
	output, err := cmd.Output()
	if err != nil {
		return "unknown"
	}

	version := strings.TrimSpace(string(output))
	if version == "" {
		return "unknown"
	}

	return version
}

func (c *Config) loadFromFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	detectedID := c.ResourceID
	detectedIP := c.ResourceIP

	if err := yaml.Unmarshal(data, c); err != nil {
		return err
	}

	// Restore detected values if config file has empty ones
	if c.ResourceID == "" && detectedID != "" {
		c.ResourceID = detectedID
	}
	if c.ResourceIP == "" && detectedIP != "" {
		c.ResourceIP = detectedIP
	}

	return nil
}

func (c *Config) loadFromEnv() {
	// Only essential environment variables
	if val := os.Getenv("SC_METADATA_ENDPOINT"); val != "" {
		c.MetadataServiceEndpoint = val
	}
	
	// Support explicit resource ID override (for testing)
	if val := os.Getenv("SC_RESOURCE_ID"); val != "" {
		c.ResourceID = val
	}
	
	// Support Resource IP Override
	if val := os.Getenv("SC_RESOURCE_IP"); val != "" {
        c.ResourceIP = val
    }
	
	// Agent version
	if val := os.Getenv("SC_AGENT_VERSION"); val != "" {
		c.AgentVersion = val
	}
	
	// Optional overrides for debugging
	if val := os.Getenv("SC_LOG_LEVEL"); val != "" {
		c.LogLevel = val
	}
}

func (c *Config) validate() error {
	if c.MetadataServiceEndpoint == "" {
		return fmt.Errorf("metadata_service_endpoint cannot be empty")
	}


	if c.ResourceID == "" {
		return errors.New("resource_id cannot be determined automatically, please set SC_RESOURCE_ID")
	}

	if c.ResourceIP == "" {
		return errors.New("resource_ip cannot be determined automatically, please set SC_RESOURCE_IP")
	}

	if c.HTTPTimeout <= 0 {
		return fmt.Errorf("http_timeout must be positive")
	}

	if c.MaxRetries < 0 {
		return fmt.Errorf("max_retries cannot be negative")
	}

	if c.RetryInterval <= 0 {
		return fmt.Errorf("retry_interval must be positive")
	}

	if c.KeyCleanupInterval <= 0 {
		return fmt.Errorf("key_cleanup_interval must be positive")
	}

	if c.Console.HTTPNotifyPort <= 0 || c.Console.HTTPNotifyPort > 65535 {
		return fmt.Errorf("http_notify_port must be between 1 and 65535")
	}


	if c.Console.SignatureTimeout <= 0 {
		return fmt.Errorf("signature_timeout must be positive")
	}

	validLogLevels := []string{"debug", "info", "warn", "error"}
	validLevel := false
	for _, level := range validLogLevels {
		if strings.ToLower(c.LogLevel) == level {
			validLevel = true
			break
		}
	}
	if !validLevel {
		return fmt.Errorf("invalid log_level: %s", c.LogLevel)
	}

	return nil
}
