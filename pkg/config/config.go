package config

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
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

// agentConfigPath is where the installer saves agent.yaml, which holds the
// metadata service address. A variable so tests can use a temporary file.
var agentConfigPath = "/etc/strettchcloud/config/agent.yaml"

// settingsPath is the settings file the package installs; SC_AGENT_CONFIG
// replaces it. A variable so tests can use a temporary file.
var settingsPath = "/etc/sc-console-agent/config.yaml"

// readMetadataBaseURL returns the metadata service base URL from agent.yaml.
func readMetadataBaseURL() (string, error) {
	data, err := os.ReadFile(agentConfigPath)
	if err != nil {
		return "", fmt.Errorf("read config: %w", err)
	}

	var config AgentConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return "", fmt.Errorf("parse config %s: %w", agentConfigPath, err)
	}

	if config.MetadataService.BaseURL == "" {
		return "", fmt.Errorf("metadata base_url not configured in %s", agentConfigPath)
	}

	return config.MetadataService.BaseURL, nil
}

func DefaultConfig() *Config {
	return &Config{
		ResourceID: detectResourceID(),
		ResourceIP: detectResourceIP(),
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
	}
}

func Load() (*Config, error) {
	cfg := DefaultConfig()

	configPath := os.Getenv("SC_AGENT_CONFIG")
	configPathRequired := configPath != ""
	if !configPathRequired {
		configPath = settingsPath
	}
	metadataBaseURL, err := cfg.loadFromFile(configPath)
	// The package installs the settings file, but the agent also runs without it.
	if err != nil && (configPathRequired || !errors.Is(err, fs.ErrNotExist)) {
		return nil, fmt.Errorf("failed to load config file %s: %w", configPath, err)
	}

	// Override with environment variables
	cfg.loadFromEnv()

	if cfg.MetadataServiceEndpoint == "" {
		if metadataBaseURL == "" {
			if metadataBaseURL, err = readMetadataBaseURL(); err != nil {
				return nil, fmt.Errorf("failed to read metadata service configuration: %w", err)
			}
		}
		cfg.MetadataServiceEndpoint = metadataBaseURL + "/metadata/v1/auth-token"
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("configuration validation failed: %w", err)
	}

	return cfg, nil
}

func detectResourceID() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	dmidecodePaths := []string{
		"/usr/sbin/dmidecode",
		"/sbin/dmidecode",
		"dmidecode",
	}

	for _, dmidecodeCmd := range dmidecodePaths {
		cmd := exec.CommandContext(ctx, dmidecodeCmd, "-s", "system-uuid")
		output, err := cmd.Output()

		if ctx.Err() == context.DeadlineExceeded {
			continue
		}

		if err != nil {
			continue
		}

		uuid := strings.TrimSpace(string(output))

		if uuid != "" && uuid != "Not Settable" && uuid != "Not Specified" &&
			!strings.HasPrefix(uuid, "00000000-0000-0000") {
			return uuid
		}
	}

	return ""
}

func detectResourceIP() string {
	// Try hostname -I first
	if cmd := exec.Command("hostname", "-I"); cmd != nil {
		if output, err := cmd.Output(); err == nil {
			ips := strings.Fields(strings.TrimSpace(string(output)))
			if len(ips) > 0 {
				return ips[0]
			}
		}
	}

	// Fallback to ip route
	if cmd := exec.Command("ip", "route", "get", "8.8.8.8"); cmd != nil {
		if output, err := cmd.Output(); err == nil {
			lines := strings.Split(string(output), "\n")
			for _, line := range lines {
				if strings.Contains(line, "src") {
					parts := strings.Fields(line)
					for i, part := range parts {
						if part == "src" && i+1 < len(parts) {
							return parts[i+1]
						}
					}
				}
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

// loadFromFile loads settings from a YAML file, and returns the metadata
// service base URL when the file sets one.
func (c *Config) loadFromFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	detectedID := c.ResourceID
	detectedIP := c.ResourceIP

	if err := yaml.Unmarshal(data, c); err != nil {
		return "", err
	}

	// Restore detected values if config file has empty ones
	if c.ResourceID == "" && detectedID != "" {
		c.ResourceID = detectedID
	}
	if c.ResourceIP == "" && detectedIP != "" {
		c.ResourceIP = detectedIP
	}

	var agentConfig AgentConfig
	if err := yaml.Unmarshal(data, &agentConfig); err != nil {
		return "", err
	}
	return agentConfig.MetadataService.BaseURL, nil
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
		return fmt.Errorf("resource_id cannot be determined: dmidecode failed. Set SC_RESOURCE_ID environment variable")
	}

	if c.ResourceIP == "" {
		return fmt.Errorf("resource_ip cannot be determined. Set SC_RESOURCE_IP environment variable")
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
