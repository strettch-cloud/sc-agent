package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	// installersConfig is what post-install.sh writes to config.yaml.
	installersConfig = "# SC-Agent Configuration\n"
	testResourceID   = "6F1C2B3A-9D4E-4F5A-8B6C-7D8E9F0A1B2C"
)

// useConfigFiles stands in for the package's config.yaml on a VM. Empty
// content means the file doesn't exist. dmidecode reports testResourceID.
func useConfigFiles(t *testing.T, settings string) {
	t.Helper()
	pointAt(t, &settingsPath, writeUnlessEmpty(t, filepath.Join(t.TempDir(), "config.yaml"), settings))
	useFakeDMIDecode(t, testResourceID)
	t.Setenv("SC_LOG_LEVEL", "")
	t.Setenv("SC_RESOURCE_ID", "")
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

func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("error = %v, want one containing %q", err, want)
	}
}

// Every setting but the log level is fixed, so nothing on a VM can point the
// agent at another token service or make it report another version.
func TestLoadIgnoresTheVariablesThatUsedToChangeSettings(t *testing.T) {
	useConfigFiles(t, "")
	elsewhere := filepath.Join(t.TempDir(), "settings.yaml")
	writeUnlessEmpty(t, elsewhere, "log_level: debug\n")
	t.Setenv("SC_METADATA_ENDPOINT", "http://elsewhere.test.invalid")
	t.Setenv("SC_AGENT_VERSION", "9.9.9")
	t.Setenv("SC_AGENT_CONFIG", elsewhere)

	cfg := mustLoad(t)

	if want := "http://169.254.169.254/metadata/v1/auth-token"; cfg.MetadataServiceEndpoint != want {
		t.Errorf("metadata endpoint = %q, want %q", cfg.MetadataServiceEndpoint, want)
	}
	if cfg.AgentVersion == "9.9.9" {
		t.Error("agent version came from SC_AGENT_VERSION")
	}
	if cfg.LogLevel != "info" {
		t.Errorf("log level = %q, want info: the file SC_AGENT_CONFIG names must not be read", cfg.LogLevel)
	}
}

func TestLoadTakesTheLogLevelFromConfigYAMLInAnyCase(t *testing.T) {
	useConfigFiles(t, "log_level: DEBUG\n")

	if cfg := mustLoad(t); cfg.LogLevel != "debug" {
		t.Errorf("log level = %q, want debug from config.yaml", cfg.LogLevel)
	}
}

func TestLoadTakesTheLogLevelFromSCLogLevelOverConfigYAML(t *testing.T) {
	useConfigFiles(t, "log_level: warn\n")
	t.Setenv("SC_LOG_LEVEL", "Error")

	if cfg := mustLoad(t); cfg.LogLevel != "error" {
		t.Errorf("log level = %q, want error from SC_LOG_LEVEL", cfg.LogLevel)
	}
}

// config.yaml is all or nothing. A file that sets anything besides a known log
// level is ignored, so the agent and the console keep working on the
// defaults, but -validate-config reports it.
func TestAConfigYAMLThatSetsMoreThanTheLogLevelIsIgnored(t *testing.T) {
	for name, settings := range map[string]string{
		"another setting":                    "console:\n  udp_knock_port: 50124\n",
		"another setting with the log level": "log_level: debug\nkey_cleanup_interval: 1s\n",
		"an unknown log level":               "log_level: verbose\n",
		"a level the logger doesn't have":    "log_level: fatal\n",
		"not YAML":                           "log_level: [\n",
	} {
		t.Run(name, func(t *testing.T) {
			useConfigFiles(t, settings)

			cfg := mustLoad(t)

			if cfg.LogLevel != "info" || cfg.Console.UDPKnockPort != 60124 {
				t.Errorf("log level %q and knock port %d, want the defaults info and 60124", cfg.LogLevel, cfg.Console.UDPKnockPort)
			}
			assertErrorContains(t, Validate(), settingsPath)
		})
	}
}

func TestAnUnknownSCLogLevelIsIgnored(t *testing.T) {
	useConfigFiles(t, "")
	t.Setenv("SC_LOG_LEVEL", "verbose")

	if cfg := mustLoad(t); cfg.LogLevel != "info" {
		t.Errorf("log level = %q, want info", cfg.LogLevel)
	}
	assertErrorContains(t, Validate(), "SC_LOG_LEVEL")
}

func TestValidateAcceptsTheConfigTheInstallerWrites(t *testing.T) {
	useConfigFiles(t, installersConfig)

	if err := Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestValidateAcceptsAMissingConfigYAML(t *testing.T) {
	useConfigFiles(t, "")

	if err := Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

// config.example.yaml is what people copy into config.yaml.
func TestValidateAcceptsTheExampleConfig(t *testing.T) {
	useConfigFiles(t, "")
	pointAt(t, &settingsPath, filepath.Join("..", "..", "config.example.yaml"))

	if err := Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

// Every VM reaches the metadata service at 169.254.169.254, so the agent reads
// the address from no file, agent.yaml included.
func TestTheMetadataAddressIsFixed(t *testing.T) {
	useConfigFiles(t, installersConfig)

	if cfg := mustLoad(t); cfg.MetadataServiceEndpoint != "http://169.254.169.254/metadata/v1/auth-token" {
		t.Errorf("metadata endpoint = %q, want http://169.254.169.254/metadata/v1/auth-token", cfg.MetadataServiceEndpoint)
	}
}

// SC_RESOURCE_ID is only a fallback: where dmidecode reads an ID, nothing can
// make the agent accept knocks meant for another VM.
func TestTheResourceIDComesFromDMIDecodeEvenWhenSCResourceIDIsSet(t *testing.T) {
	useConfigFiles(t, "")
	t.Setenv("SC_RESOURCE_ID", "another-vm")

	if cfg := mustLoad(t); cfg.ResourceID != testResourceID {
		t.Errorf("resource ID = %q, want %q from dmidecode", cfg.ResourceID, testResourceID)
	}
}

// A VM whose firmware has no ID, and which set SC_RESOURCE_ID for that reason,
// keeps working.
func TestSCResourceIDIsUsedWhenDMIDecodeFindsNoID(t *testing.T) {
	for name, dmidecodeOutput := range map[string]string{
		"no output":     "",
		"not settable":  "Not Settable",
		"not specified": "Not Specified",
		"all zeros":     "00000000-0000-0000-0000-000000000000",
	} {
		t.Run(name, func(t *testing.T) {
			useConfigFiles(t, "")
			useFakeDMIDecode(t, dmidecodeOutput)
			t.Setenv("SC_RESOURCE_ID", "resource-from-environment")

			if cfg := mustLoad(t); cfg.ResourceID != "resource-from-environment" {
				t.Errorf("resource ID = %q, want the one from SC_RESOURCE_ID", cfg.ResourceID)
			}
		})
	}
}

func TestLoadFailsWithoutAResourceID(t *testing.T) {
	useConfigFiles(t, "")
	useFakeDMIDecode(t, "Not Settable")

	_, err := Load()

	assertErrorContains(t, err, "resource_id cannot be determined")
}

func TestAConfigYAMLTheAgentCantReadIsIgnored(t *testing.T) {
	useConfigFiles(t, "")
	if err := os.Mkdir(settingsPath, 0o700); err != nil {
		t.Fatal(err)
	}

	if cfg := mustLoad(t); cfg.LogLevel != "info" {
		t.Errorf("log level = %q, want the default info", cfg.LogLevel)
	}
	assertErrorContains(t, Validate(), settingsPath)
}

// The heartbeat reports the version of the installed package.
func TestTheAgentVersionComesFromTheInstalledPackage(t *testing.T) {
	for name, testCase := range map[string]struct{ dpkgQueryOutput, want string }{
		"installed":       {"1.4.2", "1.4.2"},
		"nothing printed": {"", "unknown"},
	} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			script := "#!/bin/sh\nprintf '%s' '" + testCase.dpkgQueryOutput + "'\n"
			if err := os.WriteFile(filepath.Join(directory, "dpkg-query"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))

			if got := detectAgentVersion(); got != testCase.want {
				t.Errorf("detectAgentVersion() = %q, want %q", got, testCase.want)
			}
		})
	}
}
