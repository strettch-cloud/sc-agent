//go:build e2e

package e2e

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// These tests break what the agent depends on: the metadata service down or
// wrong, the cloud API failing, an authorized_keys it can't replace, its knock
// port taken. For each, the agent must either keep running and recover, or
// exit with code 1 and say why.

func (agent *agentContainer) waitForLog(t *testing.T, text string) {
	t.Helper()
	eventually(t, func() bool { return strings.Contains(agent.logs(t), text) },
		"the agent never logged "+strconv.Quote(text))
}

func (agent *agentContainer) assertExitedWith1Saying(t *testing.T, text string) {
	t.Helper()
	if got := agent.exitCode(t); got != "1" {
		t.Errorf("exit code = %s, want 1", got)
	}
	if logs := agent.logs(t); !strings.Contains(logs, text) {
		t.Errorf("the agent should say %q:\n%s", text, logs)
	}
}

func TestTheAgentWaitsForTheMetadataServiceWhenItStarts(t *testing.T) {
	t.Parallel()
	cloud := newFakeCloud(t)
	cloud.metadataFailures = 2
	agent := startAgent(t, cloud, agentOptions{})

	agent.waitUntilReady(t, cloud)

	if metadataRequests, _, _ := cloud.snapshot(); len(metadataRequests) < 3 {
		t.Errorf("the agent made %d metadata requests, want at least 3: two that failed, then one that worked", len(metadataRequests))
	}
}

// Without a key to check knocks against, the agent could never let the
// console in, so it exits rather than run uselessly.
func TestTheAgentExitsWithoutAUsableKnockKey(t *testing.T) {
	t.Parallel()
	for name, testCase := range map[string]struct{ knockKeyHex, logged string }{
		"no key":               {"", "metadata service did not provide public key"},
		"a key that isn't hex": {"not-a-key", "failed to set public key"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cloud := newFakeCloud(t)
			cloud.knockKeyHex = testCase.knockKeyHex
			agent := startAgent(t, cloud, agentOptions{})

			agent.assertExitedWith1Saying(t, testCase.logged)
		})
	}
}

func TestAKnockWhoseKeyRequestFailsChangesNothing(t *testing.T) {
	t.Parallel()
	cloud := newFakeCloud(t)
	cloud.keyFailures = 1
	agent := startAgent(t, cloud, agentOptions{})
	agent.addCustomerKey(t)
	agent.waitUntilReady(t, cloud)

	agent.sendKnock(t, knock(cloud.knockKey, resourceID, time.Now()))
	agent.waitForLog(t, "Failed to process knock")

	if got := agent.authorizedKeys(t); got != customerKey {
		t.Errorf("after a knock whose key request failed, authorized_keys holds:\n%s\nwant only the customer's key", got)
	}
	agent.sendKnock(t, knock(cloud.knockKey, resourceID, time.Now()))
	eventually(t, func() bool { return strings.Contains(agent.authorizedKeys(t), consoleKey) },
		"the knock after the failed one never installed the console key")
}

func TestHeartbeatsKeepComingWhenTheAPIRejectsThem(t *testing.T) {
	t.Parallel()
	cloud := newFakeCloud(t)
	cloud.heartbeatStatus = http.StatusInternalServerError
	agent := startAgent(t, cloud, agentOptions{heartbeatInterval: 200 * time.Millisecond})

	eventually(t, func() bool { _, _, heartbeats := cloud.snapshot(); return len(heartbeats) >= 3 },
		"the agent stopped sending heartbeats after the API rejected them")
	agent.waitForLog(t, "Failed to send heartbeat")

	agent.sendKnock(t, knock(cloud.knockKey, resourceID, time.Now()))
	eventually(t, func() bool { return strings.Contains(agent.authorizedKeys(t), consoleKey) },
		"after rejected heartbeats, a knock never installed the console key")
}

// When the agent can't replace authorized_keys, the knock and the cleanup
// fail, but the agent keeps running and works again once the file is fixed.
func TestAnAuthorizedKeysTheAgentCantReplaceFailsTheKnockNotTheAgent(t *testing.T) {
	t.Parallel()
	for name, breakIt := range map[string]string{
		"authorized_keys is a directory": "mkdir -p " + authorizedKeysPath + "/inside",
		".ssh is a file":                 "touch /root/.ssh",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cloud := newFakeCloud(t)
			agent := startAgent(t, cloud, agentOptions{cleanupInterval: time.Second})
			agent.waitUntilReady(t, cloud)
			agent.exec(t, "sh", "-c", breakIt)

			agent.sendKnock(t, knock(cloud.knockKey, resourceID, time.Now()))
			agent.waitForLog(t, "Failed to process knock")
			agent.waitForLog(t, "Key cleanup failed")

			agent.exec(t, "rm", "-rf", "/root/.ssh")
			agent.sendKnock(t, knock(cloud.knockKey, resourceID, time.Now()))
			// The log, not the file: cleanup runs every second and may remove
			// the key before the test reads it.
			agent.waitForLog(t, "Knock processed successfully")
		})
	}
}

func TestTheAgentExitsWhenItsKnockPortIsTaken(t *testing.T) {
	t.Parallel()
	taken, err := net.ListenPacket("udp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = taken.Close() }()
	port := taken.LocalAddr().(*net.UDPAddr).Port
	cloud := newFakeCloud(t)
	agent := startAgent(t, cloud, agentOptions{udpPort: port})

	agent.assertExitedWith1Saying(t, fmt.Sprintf("failed to listen on UDP port %d", port))
}

// Run as a user with no home directory, the agent can't tell whose
// authorized_keys to manage, so it exits rather than guess.
func TestTheAgentExitsWhenItCantFindTheUsersHome(t *testing.T) {
	t.Parallel()
	cloud := newFakeCloud(t)
	agent := startAgent(t, cloud, agentOptions{user: "4242"})

	agent.assertExitedWith1Saying(t, "failed to create key manager")
}

func TestValidateConfigAcceptsAWorkingSetup(t *testing.T) {
	t.Parallel()

	output, exitCode := validateConfig(t, map[string]string{
		agentYAMLPath:  "metadata_service:\n  base_url: http://127.0.0.1:1\n",
		configYAMLPath: "# SC-Agent Configuration\n",
		dmidecodePath:  fakeDMIDecode,
	})

	if exitCode != 0 || !strings.Contains(output, "Configuration is valid") {
		t.Errorf("-validate-config exited %d, want 0 and %q:\n%s", exitCode, "Configuration is valid", output)
	}
}

// The agent logs "Heartbeat sent successfully" at info level once it starts,
// so that line shows whether info lines are written.
func TestTheLogLevelDecidesWhichLinesAreWritten(t *testing.T) {
	t.Parallel()
	for level, wantInfoLines := range map[string]bool{"info": true, "warn": false, "error": false} {
		t.Run(level, func(t *testing.T) {
			t.Parallel()
			cloud := newFakeCloud(t)
			agent := startAgent(t, cloud, agentOptions{configYAML: "log_level: " + level + "\n"})
			agent.waitUntilReady(t, cloud)

			if wantInfoLines {
				agent.waitForLog(t, "Heartbeat sent successfully")
				return
			}
			// The heartbeat reached the fake just before the agent would log it.
			time.Sleep(500 * time.Millisecond)
			if logs := agent.logs(t); strings.Contains(logs, `"level":"info"`) {
				t.Errorf("at log level %s the agent wrote info lines:\n%s", level, logs)
			}
		})
	}
}
