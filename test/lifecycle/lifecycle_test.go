//go:build lifecycle

// Package lifecycle installs, upgrades and removes the real .deb inside a
// container that runs systemd, the way a customer's VM does. It needs Docker
// on Linux, so it only runs with -tags lifecycle (the lifecycle CI job, one OS
// per run), not in `mise run test`.
//
// Inputs, all required:
//
//	LIFECYCLE_IMAGE         base image, e.g. mirror.gcr.io/library/debian:13
//	LIFECYCLE_DEB           the .deb under test
//	LIFECYCLE_PREVIOUS_DEB  the latest released .deb, to upgrade from
package lifecycle

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testVMID     = "6f1c2b3a-9d4e-4f5a-8b6c-7d8e9f0a1b2c"
	agentService = "sc-console-agent.service"
	updaterTimer = "sc-console-agent-update-scheduler.timer"
	pollEvery    = 500 * time.Millisecond
	// The unit restarts the agent 30 s after it dies (RestartSec=30).
	restartWait = 60 * time.Second
)

var (
	imageTag            string
	packagePath         string
	previousPackagePath string
	// hostAddress is the host's address on Docker's bridge network, where the
	// containers reach the fake cloud.
	hostAddress string
)

func TestMain(m *testing.M) {
	baseImage := os.Getenv("LIFECYCLE_IMAGE")
	packagePath = os.Getenv("LIFECYCLE_DEB")
	previousPackagePath = os.Getenv("LIFECYCLE_PREVIOUS_DEB")
	if baseImage == "" || packagePath == "" || previousPackagePath == "" {
		fmt.Fprintln(os.Stderr, "LIFECYCLE_IMAGE, LIFECYCLE_DEB and LIFECYCLE_PREVIOUS_DEB must all be set")
		os.Exit(2)
	}
	var err error
	if packagePath, err = filepath.Abs(packagePath); err == nil {
		previousPackagePath, err = filepath.Abs(previousPackagePath)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	gateway, err := exec.Command("docker", "network", "inspect", "bridge", "--format", "{{(index .IPAM.Config 0).Gateway}}").CombinedOutput()
	hostAddress = strings.TrimSpace(string(gateway))
	if err != nil || net.ParseIP(hostAddress) == nil {
		fmt.Fprintf(os.Stderr, "could not find Docker's bridge gateway: %v\n%s", err, gateway)
		os.Exit(2)
	}

	imageTag = "sc-console-agent-lifecycle:" + regexp.MustCompile(`[^a-z0-9.-]+`).ReplaceAllString(strings.ToLower(baseImage), "-")
	build := exec.Command("docker", "build", "--quiet", "--build-arg", "BASE_IMAGE="+baseImage, "--tag", imageTag, ".")
	if output, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "docker build failed: %v\n%s", err, output)
		os.Exit(2)
	}
	os.Exit(m.Run())
}

// eventually reports whether condition became true within timeout.
func eventually(timeout time.Duration, condition func() bool) bool {
	deadline := time.Now().Add(timeout)
	for !condition() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(pollEvery)
	}
	return true
}

// fakeCloud stands in for everything the package talks to: the config
// repository post-install downloads agent.yaml from, the metadata service and
// the cloud API the heartbeats go to, all on one server.
type fakeCloud struct {
	server    *httptest.Server
	configURL string
	agentYAML string
	// token is what the metadata service gives the test VM, and only it, so a
	// heartbeat that carries it comes from an agent that read the VM's ID.
	token string
	// knockKey is the public key knocks are checked with. The agent won't
	// start without one.
	knockKey          string
	mutex             sync.Mutex
	heartbeatVersions []string
}

// newFakeCloud serves an agent.yaml whose metadata address is metadataURL, or
// this server's own working metadata service when metadataURL is empty.
func newFakeCloud(t *testing.T, metadataURL string) *fakeCloud {
	t.Helper()
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cloud := &fakeCloud{token: "token-" + t.Name(), knockKey: hex.EncodeToString(publicKey)}
	cloud.server = httptest.NewUnstartedServer(http.HandlerFunc(cloud.serve))
	// httptest listens on 127.0.0.1, which a container on the bridge network
	// can't reach.
	listener, err := net.Listen("tcp", net.JoinHostPort(hostAddress, "0"))
	if err != nil {
		t.Fatal(err)
	}
	cloud.server.Listener = listener
	cloud.server.Start()
	t.Cleanup(cloud.server.Close)

	cloud.configURL = cloud.server.URL
	if metadataURL == "" {
		metadataURL = cloud.server.URL
	}
	cloud.agentYAML = fmt.Sprintf("metadata_service:\n  base_url: %q\n", metadataURL)
	return cloud
}

func (cloud *fakeCloud) serve(response http.ResponseWriter, request *http.Request) {
	switch {
	// download-config.sh fetches <repo>/<environment>/agent.yaml.
	case request.Method == http.MethodGet && request.URL.Path == "/production/agent.yaml":
		_, _ = response.Write([]byte(cloud.agentYAML))
	case request.Method == http.MethodGet && request.URL.Path == "/metadata/v1/auth-token":
		if request.Header.Get("X-Resource-ID") != testVMID {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]string{
			"token": cloud.token, "cloudAPIUrl": cloud.server.URL, "publicKey": cloud.knockKey,
		})
	case request.Method == http.MethodPost && request.URL.Path == "/resource-manager/api/v1/compute/agent/heartbeat":
		var heartbeat struct {
			Version string `json:"version"`
		}
		if request.Header.Get("Authorization") != "Bearer "+cloud.token || json.NewDecoder(request.Body).Decode(&heartbeat) != nil {
			http.Error(response, "rejected", http.StatusUnauthorized)
			return
		}
		cloud.mutex.Lock()
		cloud.heartbeatVersions = append(cloud.heartbeatVersions, heartbeat.Version)
		cloud.mutex.Unlock()
		_, _ = response.Write([]byte("{}"))
	default:
		http.NotFound(response, request)
	}
}

// heartbeatsFrom counts the test VM's heartbeats from an agent that started
// with this package version installed.
func (cloud *fakeCloud) heartbeatsFrom(version string) int {
	cloud.mutex.Lock()
	defer cloud.mutex.Unlock()
	count := 0
	for _, heartbeatVersion := range cloud.heartbeatVersions {
		if heartbeatVersion == version {
			count++
		}
	}
	return count
}

// requireHeartbeat waits for more than after heartbeats from version. The
// agent sends one each time it starts and then one every 5 minutes, so a new
// one shows the agent started and works, which `systemctl is-active` doesn't:
// the agent counts as active while it still retries the metadata service.
func (cloud *fakeCloud) requireHeartbeat(t *testing.T, version string, after int) {
	t.Helper()
	if !eventually(60*time.Second, func() bool { return cloud.heartbeatsFrom(version) > after }) {
		t.Fatalf("no heartbeat from an agent running version %s", version)
	}
}

type container struct {
	name string
}

// startContainer boots a fresh container with systemd running and both
// packages mounted at /packages, and removes it when the test ends.
func startContainer(t *testing.T) *container {
	t.Helper()
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	box := &container{name: "sc-lifecycle-" + hex.EncodeToString(suffix)}
	// Each container gets its own network, not the host's: the agent always
	// listens on UDP port 60124, so agents in containers that share the host's
	// network, as the tests run side by side, would fight over it. Only the
	// e2e build lets a test move that port, and the package ships the release
	// build.
	run := exec.Command("docker", "run", "--detach", "--name", box.name,
		// systemd as PID 1 needs to manage cgroups and mount tmpfs on /run.
		"--privileged", "--cgroupns=host", "--volume", "/sys/fs/cgroup:/sys/fs/cgroup:rw",
		"--tmpfs", "/run", "--tmpfs", "/run/lock",
		"--volume", packagePath+":/packages/new.deb:ro",
		"--volume", previousPackagePath+":/packages/previous.deb:ro",
		imageTag)
	if output, err := run.CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v\n%s", err, output)
	}
	t.Cleanup(func() {
		if t.Failed() {
			journal, _ := box.exec(t, nil, "journalctl", "--no-pager", "--lines=100", "--unit="+agentService)
			t.Logf("journal of %s:\n%s", agentService, journal)
		}
		_ = exec.Command("docker", "rm", "--force", box.name).Run()
	})

	booted := eventually(60*time.Second, func() bool {
		state, _ := box.exec(t, nil, "systemctl", "is-system-running")
		return state == "running" || state == "degraded"
	})
	if !booted {
		// The container may have exited, which also takes its journal with it.
		logs, _ := exec.Command("docker", "logs", "--tail", "50", box.name).CombinedOutput()
		t.Fatalf("systemd did not finish booting; container output:\n%s", logs)
	}
	return box
}

// exec runs a command in the container and returns its trimmed output and
// exit code.
func (box *container) exec(t *testing.T, environment []string, command ...string) (string, int) {
	t.Helper()
	arguments := []string{"exec"}
	for _, variable := range environment {
		arguments = append(arguments, "--env", variable)
	}
	arguments = append(arguments, box.name)
	output, err := exec.Command("docker", append(arguments, command...)...).CombinedOutput()
	exitCode := 0
	if exitError, ok := err.(*exec.ExitError); ok {
		exitCode = exitError.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(output)), exitCode
}

func (box *container) mustExec(t *testing.T, command ...string) string {
	t.Helper()
	output, exitCode := box.exec(t, nil, command...)
	if exitCode != 0 {
		t.Fatalf("%s failed:\n%s", strings.Join(command, " "), output)
	}
	return output
}

// configureAgent gives the container what a VM's firmware provides: dmidecode
// prints the VM ID. The real one would read the host's firmware, or nothing.
func (box *container) configureAgent(t *testing.T) {
	t.Helper()
	box.mustExec(t, "sh", "-c", "printf '#!/bin/sh\\necho "+testVMID+"\\n' > /usr/sbin/dmidecode && chmod 755 /usr/sbin/dmidecode")
}

// install installs a package the way an operator does, with post-install
// pointed at the fake config repository.
func (box *container) install(t *testing.T, cloud *fakeCloud, packageFile string, extraArguments ...string) {
	t.Helper()
	command := append([]string{"apt-get", "install", "--yes"}, extraArguments...)
	command = append(command, "/packages/"+packageFile)
	output, exitCode := box.exec(t, []string{"DEBIAN_FRONTEND=noninteractive", "SC_CONFIG_REPO_URL=" + cloud.configURL}, command...)
	if exitCode != 0 {
		t.Fatalf("installing %s failed:\n%s", packageFile, output)
	}
}

func (box *container) unitState(t *testing.T, unit string) (active string, enabled string) {
	t.Helper()
	active, _ = box.exec(t, nil, "systemctl", "is-active", unit)
	enabled, _ = box.exec(t, nil, "systemctl", "is-enabled", unit)
	return active, enabled
}

func (box *container) requireRunningAndEnabled(t *testing.T, unit string) {
	t.Helper()
	var active, enabled string
	if !eventually(30*time.Second, func() bool {
		active, enabled = box.unitState(t, unit)
		return active == "active" && enabled == "enabled"
	}) {
		t.Fatalf("%s is %s and %s, not active and enabled", unit, active, enabled)
	}
}

func (box *container) mainPID(t *testing.T) string {
	t.Helper()
	return box.mustExec(t, "systemctl", "show", "--property=MainPID", "--value", agentService)
}

func TestFreshInstallReinstallCrashAndRemove(t *testing.T) {
	t.Parallel()
	cloud := newFakeCloud(t, "")
	box := startContainer(t)
	box.configureAgent(t)
	box.install(t, cloud, "new.deb")
	version := box.packageVersion(t, "/packages/new.deb")

	t.Run("installs its files and directories", func(t *testing.T) {
		for _, executable := range []string{
			"/usr/bin/sc-console-agent",
			"/usr/bin/sc-console-agent-updater.sh",
			"/usr/lib/sc-console-agent/download-config.sh",
		} {
			box.mustExec(t, "test", "-x", executable)
		}
		box.mustExec(t, "test", "-s", "/usr/lib/sc-console-agent/apt-keyring.gpg")
		for directory, mode := range map[string]string{
			"/etc/sc-console-agent":     "755",
			"/var/run/sc-console-agent": "750",
		} {
			if got := box.mustExec(t, "stat", "--format=%a", directory); got != mode {
				t.Errorf("%s has mode %s, want %s", directory, got, mode)
			}
		}
	})

	t.Run("downloads agent.yaml from the config repository", func(t *testing.T) {
		if got := box.mustExec(t, "cat", "/etc/strettchcloud/config/agent.yaml"); got != strings.TrimSpace(cloud.agentYAML) {
			t.Errorf("agent.yaml is %q, want %q", got, cloud.agentYAML)
		}
	})

	// --validate-config is how an operator checks config.yaml before a
	// restart, so it must accept the file the installer writes.
	t.Run("validates the config the installer writes", func(t *testing.T) {
		if output, exitCode := box.exec(t, nil, "sc-console-agent", "--validate-config"); exitCode != 0 {
			t.Errorf("--validate-config exited %d:\n%s", exitCode, output)
		}
	})

	t.Run("starts the agent and its update timer", func(t *testing.T) {
		box.requireRunningAndEnabled(t, agentService)
		box.requireRunningAndEnabled(t, updaterTimer)
	})

	t.Run("the running service sends a heartbeat", func(t *testing.T) {
		cloud.requireHeartbeat(t, version, 0)
	})

	t.Run("reinstalling the same version keeps it running", func(t *testing.T) {
		box.install(t, cloud, "new.deb", "--reinstall")
		box.requireRunningAndEnabled(t, agentService)
		box.requireRunningAndEnabled(t, updaterTimer)
	})

	t.Run("systemd restarts the agent after it is killed", func(t *testing.T) {
		killedPID := box.mainPID(t)
		heartbeatsBefore := cloud.heartbeatsFrom(version)
		box.mustExec(t, "kill", "-KILL", killedPID)
		restarted := eventually(restartWait, func() bool {
			active, _ := box.unitState(t, agentService)
			pid := box.mainPID(t)
			return active == "active" && pid != "0" && pid != killedPID
		})
		if !restarted {
			t.Fatal("systemd did not restart the agent")
		}
		cloud.requireHeartbeat(t, version, heartbeatsBefore)
	})

	// config.yaml may only set the log level. A file that sets more must not
	// stop the agent: it is ignored, and --validate-config reports it.
	t.Run("ignores a config.yaml that sets more than the log level", func(t *testing.T) {
		box.mustExec(t, "sh", "-c", "printf 'log_level: debug\\nheartbeat_interval: 1ms\\n' > /etc/sc-console-agent/config.yaml")
		output, exitCode := box.exec(t, nil, "sc-console-agent", "--validate-config")
		if exitCode != 1 || !strings.Contains(output, "may only set log_level") {
			t.Errorf("--validate-config exited %d, want 1 naming the rule:\n%s", exitCode, output)
		}

		heartbeatsBefore := cloud.heartbeatsFrom(version)
		box.mustExec(t, "systemctl", "restart", agentService)
		box.requireRunningAndEnabled(t, agentService)
		cloud.requireHeartbeat(t, version, heartbeatsBefore)
		journal := box.mustExec(t, "journalctl", "--no-pager", "--unit", agentService)
		if !strings.Contains(journal, "Ignoring /etc/sc-console-agent/config.yaml may only set log_level") {
			t.Errorf("the journal doesn't say the file was ignored:\n%s", journal)
		}
	})

	t.Run("apt remove stops it and keeps the configuration", func(t *testing.T) {
		box.mustExec(t, "sh", "-c", "echo '# an operator edit apt remove must keep' >> /etc/sc-console-agent/config.yaml")
		agentConfigBefore := box.mustExec(t, "cat", "/etc/sc-console-agent/config.yaml")
		agentYAMLBefore := box.mustExec(t, "cat", "/etc/strettchcloud/config/agent.yaml")

		output, exitCode := box.exec(t, []string{"DEBIAN_FRONTEND=noninteractive"}, "apt-get", "remove", "--yes", "sc-console-agent")
		if exitCode != 0 {
			t.Fatalf("apt-get remove failed:\n%s", output)
		}
		for _, unit := range []string{agentService, updaterTimer} {
			if active, enabled := box.unitState(t, unit); active == "active" || enabled == "enabled" {
				t.Errorf("%s is still %s and %s", unit, active, enabled)
			}
		}
		if _, exitCode := box.exec(t, nil, "test", "-e", "/usr/bin/sc-console-agent"); exitCode == 0 {
			t.Error("the binary is still installed")
		}
		if got := box.mustExec(t, "cat", "/etc/sc-console-agent/config.yaml"); got != agentConfigBefore {
			t.Errorf("config.yaml changed to:\n%s", got)
		}
		if got := box.mustExec(t, "cat", "/etc/strettchcloud/config/agent.yaml"); got != agentYAMLBefore {
			t.Errorf("agent.yaml changed to:\n%s", got)
		}
	})
}

// During an image build (Packer, for example) no metadata service answers.
// The install must still succeed and leave the agent enabled for first boot.
func TestInstallSucceedsWithoutAMetadataService(t *testing.T) {
	t.Parallel()
	// Nothing listens on port 1, so every metadata request fails.
	cloud := newFakeCloud(t, "http://127.0.0.1:1")
	box := startContainer(t)
	box.configureAgent(t)

	box.install(t, cloud, "new.deb")

	for _, unit := range []string{agentService, updaterTimer} {
		if _, enabled := box.unitState(t, unit); enabled != "enabled" {
			t.Errorf("%s is %s, not enabled", unit, enabled)
		}
	}
}

func TestUpgradeFromTheLatestRelease(t *testing.T) {
	t.Parallel()
	cloud := newFakeCloud(t, "")
	box := startContainer(t)
	box.configureAgent(t)
	box.install(t, cloud, "previous.deb")
	box.requireRunningAndEnabled(t, agentService)
	previousPID := box.mainPID(t)

	box.install(t, cloud, "new.deb")

	newVersion := box.packageVersion(t, "/packages/new.deb")
	if installed := box.installedVersion(t); installed != newVersion {
		t.Errorf("version %s is installed, want %s", installed, newVersion)
	}
	box.requireRunningAndEnabled(t, agentService)
	box.requireRunningAndEnabled(t, updaterTimer)

	// dpkg replaced the binary, so a process still running the old one shows
	// its executable as "(deleted)".
	pid := box.mainPID(t)
	if pid == previousPID {
		t.Error("the agent was not restarted")
	}
	if executable := box.mustExec(t, "readlink", "/proc/"+pid+"/exe"); executable != "/usr/bin/sc-console-agent" {
		t.Errorf("the agent runs %s", executable)
	}
	if got := box.mustExec(t, "cat", "/etc/strettchcloud/config/agent.yaml"); got != strings.TrimSpace(cloud.agentYAML) {
		t.Errorf("agent.yaml is %q, want %q", got, cloud.agentYAML)
	}

	cloud.requireHeartbeat(t, newVersion, 0)
}
