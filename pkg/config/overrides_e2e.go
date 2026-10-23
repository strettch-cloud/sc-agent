//go:build e2e

package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// The end-to-end tests build the agent with the e2e tag, which compiles this
// file in. It lets them run several agents side by side against fake servers,
// with short intervals. Release builds don't compile it, so none of this can
// be changed on a VM.

func init() {
	if path := os.Getenv("SC_TEST_AGENT_YAML"); path != "" {
		agentConfigPath = path
	}
	if path := os.Getenv("SC_TEST_CONFIG_YAML"); path != "" {
		settingsPath = path
	}
	if path := os.Getenv("SC_TEST_DMIDECODE"); path != "" {
		dmidecodePaths = []string{path}
	}
}

func applyTestOverrides(cfg *Config) {
	for name, setting := range map[string]*time.Duration{
		"SC_TEST_HTTP_TIMEOUT":         &cfg.HTTPTimeout,
		"SC_TEST_KEY_CLEANUP_INTERVAL": &cfg.KeyCleanupInterval,
		"SC_TEST_RETRY_INTERVAL":       &cfg.RetryInterval,
	} {
		if value := os.Getenv(name); value != "" {
			duration, err := time.ParseDuration(value)
			if err != nil {
				panic(fmt.Sprintf("%s=%q: %v", name, value, err))
			}
			*setting = duration
		}
	}
	for name, setting := range map[string]*int{
		"SC_TEST_UDP_KNOCK_PORT":   &cfg.Console.UDPKnockPort,
		"SC_TEST_HTTP_NOTIFY_PORT": &cfg.Console.HTTPNotifyPort,
		"SC_TEST_MAX_RETRIES":      &cfg.MaxRetries,
	} {
		if value := os.Getenv(name); value != "" {
			number, err := strconv.Atoi(value)
			if err != nil {
				panic(fmt.Sprintf("%s=%q: %v", name, value, err))
			}
			*setting = number
		}
	}
	if version := os.Getenv("SC_TEST_AGENT_VERSION"); version != "" {
		cfg.AgentVersion = version
	}
}
