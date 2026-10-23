package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const agentYAMLWithAddress = "metadata_service:\n  base_url: http://metadata.test.invalid\n"

// useConfigFiles stands in for a VM's two files: agent.yaml, which the
// installer downloads with the metadata address, and the package's
// config.yaml. Empty content means the file doesn't exist.
func useConfigFiles(t *testing.T, agentYAML, settings string) {
	t.Helper()
	directory := t.TempDir()
	pointAt(t, &agentConfigPath, writeUnlessEmpty(t, filepath.Join(directory, "agent.yaml"), agentYAML))
	pointAt(t, &settingsPath, writeUnlessEmpty(t, filepath.Join(directory, "config.yaml"), settings))
	t.Setenv("SC_AGENT_CONFIG", "")
	t.Setenv("SC_METADATA_ENDPOINT", "")
	t.Setenv("SC_LOG_LEVEL", "")
	// What dmidecode and `hostname -I` give the agent on a VM.
	t.Setenv("SC_RESOURCE_ID", "test-resource")
	t.Setenv("SC_RESOURCE_IP", "192.0.2.1")
}

func writeUnlessEmpty(t *testing.T, path, content string) string {
	t.Helper()
	if content != "" {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func pointAt(t *testing.T, variable *string, value string) {
	t.Helper()
	original := *variable
	*variable = value
	t.Cleanup(func() { *variable = original })
}

func mustLoad(t *testing.T) *Config {
	t.Helper()
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

// The installer creates config.yaml for settings, but nothing used to read it.
func TestLoadReadsThePackagesConfigFile(t *testing.T) {
	useConfigFiles(t, agentYAMLWithAddress, "console:\n  udp_knock_port: 50124\n")

	cfg := mustLoad(t)

	if cfg.Console.UDPKnockPort != 50124 {
		t.Errorf("UDP knock port = %d, want 50124 from config.yaml", cfg.Console.UDPKnockPort)
	}
	if want := "http://metadata.test.invalid/metadata/v1/auth-token"; cfg.MetadataServiceEndpoint != want {
		t.Errorf("metadata endpoint = %q, want %q from agent.yaml", cfg.MetadataServiceEndpoint, want)
	}
}

func TestLoadRunsWithoutThePackagesConfigFile(t *testing.T) {
	useConfigFiles(t, agentYAMLWithAddress, "")

	cfg := mustLoad(t)

	if cfg.Console.UDPKnockPort != 60124 {
		t.Errorf("UDP knock port = %d, want the default 60124", cfg.Console.UDPKnockPort)
	}
}

func TestLoadLetsAVariableWinOverTheFile(t *testing.T) {
	useConfigFiles(t, agentYAMLWithAddress, "log_level: warn\n")
	t.Setenv("SC_LOG_LEVEL", "debug")

	if cfg := mustLoad(t); cfg.LogLevel != "debug" {
		t.Errorf("log level = %q, want debug from SC_LOG_LEVEL", cfg.LogLevel)
	}
}

// A settings file may carry its own metadata address, as SC_AGENT_CONFIG files
// did when they replaced agent.yaml.
func TestLoadTakesTheMetadataAddressFromTheSettingsFileFirst(t *testing.T) {
	useConfigFiles(t, agentYAMLWithAddress, "metadata_service:\n  base_url: http://other.test.invalid\n")

	if cfg := mustLoad(t); cfg.MetadataServiceEndpoint != "http://other.test.invalid/metadata/v1/auth-token" {
		t.Errorf("metadata endpoint = %q, want the settings file's address", cfg.MetadataServiceEndpoint)
	}
}

// Without an address the agent used to panic, -validate-config included.
func TestLoadFailsWithoutAMetadataAddress(t *testing.T) {
	useConfigFiles(t, "", "# SC-Agent Configuration\n")

	_, err := Load()

	if err == nil || !strings.Contains(err.Error(), agentConfigPath) {
		t.Errorf("Load error = %v, want one naming %s", err, agentConfigPath)
	}
}

// The package's own settings file is optional, but a file named in
// SC_AGENT_CONFIG must exist: a typo there shouldn't silently fall back.
func TestLoadRequiresTheFileSCAgentConfigNames(t *testing.T) {
	useConfigFiles(t, agentYAMLWithAddress, "")
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	t.Setenv("SC_AGENT_CONFIG", missing)

	_, err := Load()

	if err == nil || !strings.Contains(err.Error(), missing) {
		t.Errorf("Load error = %v, want one naming %s", err, missing)
	}
}

func TestLoadRejectsASettingsFileThatIsNotYAML(t *testing.T) {
	useConfigFiles(t, agentYAMLWithAddress, "console: [\n")

	_, err := Load()

	if err == nil || !strings.Contains(err.Error(), settingsPath) {
		t.Errorf("Load error = %v, want one naming %s", err, settingsPath)
	}
}

// config.example.yaml is what people copy into config.yaml, so all of it must
// load, and none of it may change which VM the agent says it is, its version,
// or where it gets its token.
func TestLoadAcceptsTheWholeExampleConfig(t *testing.T) {
	example, err := os.ReadFile(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	useConfigFiles(t, agentYAMLWithAddress, string(example))

	cfg := mustLoad(t)

	if want := "http://metadata.test.invalid/metadata/v1/auth-token"; cfg.MetadataServiceEndpoint != want {
		t.Errorf("metadata endpoint = %q, want %q from agent.yaml", cfg.MetadataServiceEndpoint, want)
	}
	var exampleSettings Config
	if err := yaml.Unmarshal(example, &exampleSettings); err != nil {
		t.Fatal(err)
	}
	if exampleSettings.ResourceID != "" || exampleSettings.ResourceIP != "" ||
		exampleSettings.AgentVersion != "" || exampleSettings.MetadataServiceEndpoint != "" {
		t.Errorf("the example sets values that belong to one VM: resource_id %q, resource_ip %q, agent_version %q, metadata_service_endpoint %q",
			exampleSettings.ResourceID, exampleSettings.ResourceIP, exampleSettings.AgentVersion, exampleSettings.MetadataServiceEndpoint)
	}
}
