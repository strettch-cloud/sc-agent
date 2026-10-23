package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config holds the agent's settings. Only LogLevel can be changed on a VM;
// every other setting is fixed in DefaultConfig, so a mistaken edit on many
// VMs can't make their agents overload the platform or weaken the console.
type Config struct {
	// Service endpoints
	MetadataServiceEndpoint string

	// Resource identification
	ResourceID string

	Console ConsoleConfig

	// HTTP settings
	HTTPTimeout time.Duration

	// Operational settings
	LogLevel           string
	KeyCleanupInterval time.Duration
	MaxRetries         int
	RetryInterval      time.Duration
	AgentVersion       string
}

type ConsoleConfig struct {
	UDPKnockPort     int
	HTTPNotifyPort   int
	MaxPacketSize    int
	SignatureTimeout int
}

// AgentConfig represents the structure of agent.yaml
type AgentConfig struct {
	MetadataService struct {
		BaseURL string `yaml:"base_url"`
	} `yaml:"metadata_service"`
}

// These are variables only so tests can use temporary files and a fake
// dmidecode.
var (
	// agentConfigPath is where the installer saves agent.yaml, which holds the
	// metadata service address.
	agentConfigPath = "/etc/strettchcloud/config/agent.yaml"
	// settingsPath is the package's config.yaml, which may set the log level
	// and nothing else.
	settingsPath = "/etc/sc-console-agent/config.yaml"
	// dmidecodePaths are tried in order to read the resource ID.
	dmidecodePaths = []string{"/usr/sbin/dmidecode", "/sbin/dmidecode", "dmidecode"}
)

// validLogLevels are the names the logger knows.
var validLogLevels = []string{"debug", "info", "warn", "error"}

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

// DefaultConfig returns the agent's fixed settings.
func DefaultConfig() *Config {
	return &Config{
		ResourceID: detectResourceID(),
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

// Load returns the configuration the agent runs with. A config.yaml or
// SC_LOG_LEVEL the agent can't use is ignored with a warning, so a bad edit
// can't stop the agent and the console with it.
func Load() (*Config, error) {
	return load(false)
}

// Validate checks the configuration as Load would, but fails on a config.yaml
// or SC_LOG_LEVEL the agent can't use instead of ignoring it, so
// -validate-config reports them.
func Validate() error {
	_, err := load(true)
	return err
}

func load(strict bool) (*Config, error) {
	cfg := DefaultConfig()
	applyTestOverrides(cfg)

	settingsErrors := []error{cfg.applySettingsFile(settingsPath)}
	if level := os.Getenv("SC_LOG_LEVEL"); level != "" {
		settingsErrors = append(settingsErrors, cfg.setLogLevel(level, "SC_LOG_LEVEL"))
	}
	if err := errors.Join(settingsErrors...); err != nil {
		if strict {
			return nil, err
		}
		log.Printf("Ignoring %v", err)
	}

	metadataBaseURL, err := readMetadataBaseURL()
	if err != nil {
		return nil, fmt.Errorf("failed to read metadata service configuration: %w", err)
	}
	cfg.MetadataServiceEndpoint = metadataBaseURL + "/metadata/v1/auth-token"

	if cfg.ResourceID == "" {
		return nil, fmt.Errorf("configuration validation failed: resource_id cannot be determined: dmidecode failed. Set SC_RESOURCE_ID in /etc/sc-console-agent/environment")
	}

	return cfg, nil
}

// applySettingsFile applies config.yaml, which may set log_level and nothing
// else. A missing file changes nothing. Any other file that breaks that rule
// is rejected whole, so an edit is never half applied.
func (c *Config) applySettingsFile(path string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	var settings struct {
		LogLevel string `yaml:"log_level"`
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	// A file with only comments has no YAML document, which is fine.
	if err := decoder.Decode(&settings); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s may only set log_level: %w", path, err)
	}
	if settings.LogLevel == "" {
		return nil
	}
	return c.setLogLevel(settings.LogLevel, path)
}

// setLogLevel sets a level the logger knows, in any case. source names where
// the level came from, for the error.
func (c *Config) setLogLevel(level, source string) error {
	normalized := strings.ToLower(strings.TrimSpace(level))
	if !slices.Contains(validLogLevels, normalized) {
		return fmt.Errorf("%s: unknown log level %q, expected one of %s", source, level, strings.Join(validLogLevels, ", "))
	}
	c.LogLevel = normalized
	return nil
}

// detectResourceID reads the resource ID with dmidecode. SC_RESOURCE_ID is
// used only when dmidecode finds none, so it can't make a VM act as another
// one, and a VM that already needed it keeps working.
func detectResourceID() string {
	if resourceID := resourceIDFromDMIDecode(); resourceID != "" {
		return resourceID
	}
	return os.Getenv("SC_RESOURCE_ID")
}

func resourceIDFromDMIDecode() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

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
