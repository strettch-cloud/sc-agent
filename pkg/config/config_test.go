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

	if want := "http://169.254.169.254/metadata/v1/auth-token"; cfg.MetadataServiceEndpoint != want {
		t.Errorf("MetadataServiceEndpoint = %q, want %q", cfg.MetadataServiceEndpoint, want)
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
