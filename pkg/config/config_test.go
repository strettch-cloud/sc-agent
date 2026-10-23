package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// These values are fixed on every VM, so a change here changes every agent's
// load on the platform and how the console accepts knocks.
func TestDefaultConfig(t *testing.T) {
	useFakeDMIDecode(t, "")

	cfg := DefaultConfig()

	if cfg.MetadataServiceEndpoint != "" {
		t.Errorf("MetadataServiceEndpoint = %q, want it left for Load to fill in", cfg.MetadataServiceEndpoint)
	}
	want := ConsoleConfig{UDPKnockPort: 60124, MaxPacketSize: 1024, SignatureTimeout: 30}
	if cfg.Console != want {
		t.Errorf("Console = %+v, want %+v", cfg.Console, want)
	}
	if cfg.HTTPTimeout != 30*time.Second || cfg.KeyCleanupInterval != 120*time.Second {
		t.Errorf("HTTP timeout %v and cleanup interval %v, want 30s and 2m0s", cfg.HTTPTimeout, cfg.KeyCleanupInterval)
	}
	if cfg.HeartbeatInterval != 5*time.Minute {
		t.Errorf("heartbeat interval = %v, want 5m0s", cfg.HeartbeatInterval)
	}
	if cfg.MaxRetries != 3 || cfg.RetryInterval != 5*time.Second {
		t.Errorf("%d retries every %v, want 3 every 5s", cfg.MaxRetries, cfg.RetryInterval)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("log level = %q, want info", cfg.LogLevel)
	}
}

func TestReadMetadataBaseURL(t *testing.T) {
	for name, testCase := range map[string]struct {
		agentYAML string
		wantURL   string
	}{
		"the link-local address": {agentYAML: "metadata_service:\n  base_url: http://169.254.169.254\n", wantURL: "http://169.254.169.254"},
		"an address with a port": {agentYAML: "metadata_service:\n  base_url: http://172.22.10.12:8000\n", wantURL: "http://172.22.10.12:8000"},
		"no file":                {},
		"not YAML":               {agentYAML: "invalid: yaml: content:"},
		"an empty base_url":      {agentYAML: "metadata_service:\n  base_url: \"\"\n"},
		"no metadata_service":    {agentYAML: "other_config:\n  value: test\n"},
	} {
		t.Run(name, func(t *testing.T) {
			pointAt(t, &agentConfigPath, writeUnlessEmpty(t, filepath.Join(t.TempDir(), "agent.yaml"), testCase.agentYAML))

			url, err := readMetadataBaseURL()

			if testCase.wantURL == "" {
				if err == nil {
					t.Errorf("readMetadataBaseURL() = %q, want an error", url)
				}
				return
			}
			if err != nil || url != testCase.wantURL {
				t.Errorf("readMetadataBaseURL() = %q, %v, want %q", url, err, testCase.wantURL)
			}
		})
	}
}

// useFakeDMIDecode makes dmidecode print output, as a VM's firmware would.
func useFakeDMIDecode(t *testing.T, output string) {
	t.Helper()
	script := writeUnlessEmpty(t, filepath.Join(t.TempDir(), "dmidecode"), "#!/bin/sh\nprintf '%s\\n' '"+output+"'\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	original := dmidecodePaths
	dmidecodePaths = []string{script}
	t.Cleanup(func() { dmidecodePaths = original })
}
