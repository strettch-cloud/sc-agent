//go:build e2e

// Package e2e runs the real agent binary against a fake metadata service and
// cloud API, the way systemd runs it on a customer's VM, and checks what it
// does: the heartbeat, the console key it writes after a knock, which knocks
// it ignores, and how it stops.
//
// The agent writes console keys to the authorized_keys of the user running it,
// and nothing can point it elsewhere. So it runs as root in a throwaway
// container, never on the machine running the tests. The container shares the
// host's network and reaches the fakes on 127.0.0.1. It needs Docker on Linux:
// run it with `mise run test-e2e`.
package e2e

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	resourceID   = "3b9f2c1d-7e6a-4d5b-9c8e-1f2a3b4c5d6e"
	agentVersion = "1.2.3-e2e"
	image        = "mirror.gcr.io/library/debian:13"

	// Public halves of two throwaway keys: one the console API hands out, and
	// one a customer added to root's authorized_keys themselves.
	consoleKey  = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIL9+rlL+7Hj6yo1r12wYllDp32neLN+VG5TBvRDUYaqV console@e2e.test"
	customerKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIMOt2eMNtlWd+XMjAVmgQ/xbdhHAzSD5M+HVBkyuaof5 customer@e2e.test"
	// The agent marks the lines it manages with these.
	consoleKeyComment = "# Strettch Cloud Console Access Key"
	consoleKeySuffix  = "-strettch_cloud_sat"

	authorizedKeysPath = "/root/.ssh/authorized_keys"
	waitForAgent       = 20 * time.Second
	pollEvery          = 100 * time.Millisecond
)

var agentBinaryDirectory string

// TestMain builds the agent once and pulls the image, so neither counts
// against a test's timeouts.
func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	directory, err := os.MkdirTemp("", "sc-agent-e2e")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = os.RemoveAll(directory) }()
	agentBinaryDirectory = directory

	build := exec.Command("go", "build", "-ldflags", "-X main.version="+agentVersion,
		"-o", filepath.Join(directory, "sc-console-agent"), "../../cmd")
	// The binary runs in a Linux container, whatever builds it.
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
	if output, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "go build failed: %v\n%s", err, output)
		return 1
	}
	if output, err := exec.Command("docker", "pull", "--quiet", image).CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "docker pull %s failed (is Docker running?): %v\n%s", image, err, output)
		return 1
	}
	return m.Run()
}

func eventually(t *testing.T, condition func() bool, failure string) {
	t.Helper()
	deadline := time.Now().Add(waitForAgent)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("after %s: %s", waitForAgent, failure)
		}
		time.Sleep(pollEvery)
	}
}

type recordedRequest struct {
	query         string
	resourceID    string
	authorization string
	body          []byte
}

// fakeCloud is the metadata service and the cloud API in one server: the
// metadata response names this same server as the cloud API.
type fakeCloud struct {
	server     *httptest.Server
	token      string
	knockKey   ed25519.PrivateKey
	keyExpires time.Duration

	mutex            sync.Mutex
	metadataRequests []recordedRequest
	keyRequests      []recordedRequest
	heartbeats       []recordedRequest
}

func newFakeCloud(t *testing.T) *fakeCloud {
	t.Helper()
	_, knockKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cloud := &fakeCloud{token: "token-" + t.Name(), knockKey: knockKey, keyExpires: time.Hour}
	cloud.server = httptest.NewServer(http.HandlerFunc(cloud.serve))
	t.Cleanup(cloud.server.Close)
	return cloud
}

func (cloud *fakeCloud) serve(response http.ResponseWriter, request *http.Request) {
	body, _ := io.ReadAll(request.Body)
	recorded := recordedRequest{
		query:         request.URL.RawQuery,
		resourceID:    request.Header.Get("X-Resource-ID"),
		authorization: request.Header.Get("Authorization"),
		body:          body,
	}

	cloud.mutex.Lock()
	defer cloud.mutex.Unlock()
	switch {
	case request.Method == http.MethodGet && request.URL.Path == "/metadata/v1/auth-token":
		cloud.metadataRequests = append(cloud.metadataRequests, recorded)
		writeJSON(response, map[string]string{
			"token":       cloud.token,
			"publicKey":   hex.EncodeToString(cloud.knockKey.Public().(ed25519.PublicKey)),
			"cloudAPIUrl": cloud.server.URL,
		})
	case request.Method == http.MethodGet && request.URL.Path == "/resource-manager/api/v1/console/public-key/agent":
		cloud.keyRequests = append(cloud.keyRequests, recorded)
		now := time.Now().UTC()
		writeJSON(response, map[string]any{"data": map[string]string{
			"publicKey":   consoleKey,
			"principal":   "root",
			"requesterId": "support@e2e.test",
			"createdAt":   now.Format(time.RFC3339),
			"expireAt":    now.Add(cloud.keyExpires).Format(time.RFC3339),
		}})
	case request.Method == http.MethodPost && request.URL.Path == "/resource-manager/api/v1/compute/agent/heartbeat":
		cloud.heartbeats = append(cloud.heartbeats, recorded)
		writeJSON(response, map[string]string{})
	default:
		http.NotFound(response, request)
	}
}

func writeJSON(response http.ResponseWriter, value any) {
	response.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(response).Encode(value)
}

func (cloud *fakeCloud) snapshot() (metadataRequests, keyRequests, heartbeats []recordedRequest) {
	cloud.mutex.Lock()
	defer cloud.mutex.Unlock()
	return append([]recordedRequest(nil), cloud.metadataRequests...),
		append([]recordedRequest(nil), cloud.keyRequests...),
		append([]recordedRequest(nil), cloud.heartbeats...)
}

// knock builds the packet the console sends: a big-endian Unix timestamp in
// seconds, then an Ed25519 signature of "<resource ID>|<timestamp>".
func knock(signingKey ed25519.PrivateKey, signedResourceID string, sentAt time.Time) []byte {
	timestamp := sentAt.Unix()
	packet := binary.BigEndian.AppendUint64(nil, uint64(timestamp))
	return append(packet, ed25519.Sign(signingKey, []byte(fmt.Sprintf("%s|%d", signedResourceID, timestamp)))...)
}

type agentOptions struct {
	// withoutResourceID leaves out SC_RESOURCE_ID, as on a VM where dmidecode
	// can't read one; the image has no dmidecode.
	withoutResourceID bool
	cleanupInterval   time.Duration
}

type agentContainer struct {
	name    string
	udpPort int
}

func freePort(t *testing.T, network string) int {
	t.Helper()
	if network == "udp" {
		listener, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = listener.Close() }()
		return listener.LocalAddr().(*net.UDPAddr).Port
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	return listener.Addr().(*net.TCPAddr).Port
}

// startAgent runs the agent in a fresh container with only the settings a
// test gives it, and removes the container when the test ends.
func startAgent(t *testing.T, cloud *fakeCloud, options agentOptions) *agentContainer {
	t.Helper()
	if options.cleanupInterval == 0 {
		options.cleanupInterval = time.Hour
	}
	agent := &agentContainer{name: "sc-agent-e2e-" + randomSuffix(t), udpPort: freePort(t, "udp")}

	// SC_AGENT_CONFIG replaces agent.yaml, so this one file holds the
	// metadata address and the agent's settings.
	configDirectory := t.TempDir()
	config := fmt.Sprintf(`metadata_service:
  base_url: %q
console:
  udp_knock_port: %d
  http_notify_port: %d
key_cleanup_interval: %q
http_timeout: "2s"
max_retries: 1
retry_interval: "100ms"
`, cloud.server.URL, agent.udpPort, freePort(t, "tcp"), options.cleanupInterval.String())
	if err := os.WriteFile(filepath.Join(configDirectory, "config.yaml"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	// The container's root must read the file whatever user created the
	// directory.
	if err := os.Chmod(configDirectory, 0o755); err != nil {
		t.Fatal(err)
	}

	arguments := []string{"run", "--detach", "--name", agent.name, "--network", "host",
		"--volume", agentBinaryDirectory + ":/agent:ro",
		"--volume", configDirectory + ":/config:ro",
		"--env", "SC_AGENT_CONFIG=/config/config.yaml",
		// What dpkg-query and `hostname -I` give the agent on a VM.
		"--env", "SC_AGENT_VERSION=" + agentVersion,
		"--env", "SC_RESOURCE_IP=127.0.0.1",
	}
	if !options.withoutResourceID {
		arguments = append(arguments, "--env", "SC_RESOURCE_ID="+resourceID)
	}
	arguments = append(arguments, image, "/agent/sc-console-agent")
	if output, err := exec.Command("docker", arguments...).CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v\n%s", err, output)
	}
	t.Cleanup(func() {
		if t.Failed() {
			logs, _ := exec.Command("docker", "logs", "--tail", "100", agent.name).CombinedOutput()
			t.Logf("agent log:\n%s", logs)
		}
		_ = exec.Command("docker", "rm", "--force", agent.name).Run()
	})
	return agent
}

func randomSuffix(t *testing.T) string {
	t.Helper()
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(suffix)
}

// exec runs a command in the agent's container and returns its output.
func (agent *agentContainer) exec(t *testing.T, command ...string) string {
	t.Helper()
	output, err := exec.Command("docker", append([]string{"exec", agent.name}, command...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(command, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func (agent *agentContainer) authorizedKeys(t *testing.T) string {
	t.Helper()
	return agent.exec(t, "sh", "-c", "cat "+authorizedKeysPath+" 2>/dev/null || true")
}

func (agent *agentContainer) addCustomerKey(t *testing.T) {
	t.Helper()
	agent.exec(t, "sh", "-c", "install -d -m 700 /root/.ssh && printf '%s\\n' '"+customerKey+"' > "+authorizedKeysPath+" && chmod 600 "+authorizedKeysPath)
}

func (agent *agentContainer) sendKnock(t *testing.T, packet []byte) {
	t.Helper()
	connection, err := net.Dial("udp", fmt.Sprintf("127.0.0.1:%d", agent.udpPort))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Close() }()
	if _, err := connection.Write(packet); err != nil {
		t.Fatal(err)
	}
}

// waitUntilReady waits for the first heartbeat. The agent sends it after it
// has the knock key and its UDP listener is up, so knocks sent after it count.
func (agent *agentContainer) waitUntilReady(t *testing.T, cloud *fakeCloud) {
	t.Helper()
	eventually(t, func() bool {
		_, _, heartbeats := cloud.snapshot()
		return len(heartbeats) > 0
	}, "the agent sent no heartbeat")
}

// exitCode waits for the container to stop and returns the agent's exit code.
func (agent *agentContainer) exitCode(t *testing.T) string {
	t.Helper()
	output, err := exec.Command("docker", "wait", agent.name).CombinedOutput()
	if err != nil {
		t.Fatalf("docker wait: %v\n%s", err, output)
	}
	return strings.TrimSpace(string(output))
}

func TestAgentSendsAHeartbeatWhenItStarts(t *testing.T) {
	t.Parallel()
	cloud := newFakeCloud(t)
	agent := startAgent(t, cloud, agentOptions{})

	agent.waitUntilReady(t, cloud)

	metadataRequests, _, heartbeats := cloud.snapshot()
	if got := metadataRequests[0].resourceID; got != resourceID {
		t.Errorf("metadata request X-Resource-ID = %q, want %q", got, resourceID)
	}
	if got := metadataRequests[0].query; got != "includePublicKey=True" {
		t.Errorf("metadata request query = %q, want the knock key included", got)
	}
	if got, want := heartbeats[0].authorization, "Bearer "+cloud.token; got != want {
		t.Errorf("heartbeat Authorization = %q, want %q", got, want)
	}
	var heartbeat struct {
		AgentType string `json:"agentType"`
		EventTime string `json:"eventTime"`
		Version   string `json:"version"`
	}
	if err := json.Unmarshal(heartbeats[0].body, &heartbeat); err != nil {
		t.Fatalf("heartbeat body %q: %v", heartbeats[0].body, err)
	}
	if heartbeat.AgentType != "SC_AGENT" || heartbeat.Version != agentVersion {
		t.Errorf("heartbeat = %+v, want agentType SC_AGENT and version %s", heartbeat, agentVersion)
	}
	if _, err := time.Parse(time.RFC3339, heartbeat.EventTime); err != nil {
		t.Errorf("heartbeat eventTime %q is not RFC 3339: %v", heartbeat.EventTime, err)
	}
}

func TestAKnockInstallsTheConsoleKeyAndKeepsTheCustomersKeys(t *testing.T) {
	t.Parallel()
	cloud := newFakeCloud(t)
	agent := startAgent(t, cloud, agentOptions{})
	agent.addCustomerKey(t)
	agent.waitUntilReady(t, cloud)

	agent.sendKnock(t, knock(cloud.knockKey, resourceID, time.Now()))

	eventually(t, func() bool { return strings.Contains(agent.authorizedKeys(t), consoleKey) },
		"the console key never reached "+authorizedKeysPath)
	_, keyRequests, _ := cloud.snapshot()
	if got, want := keyRequests[0].authorization, "Bearer "+cloud.token; got != want {
		t.Errorf("key request Authorization = %q, want %q", got, want)
	}
	lines := strings.Split(agent.authorizedKeys(t), "\n")
	if len(lines) != 3 || lines[0] != customerKey || lines[1] != consoleKeyComment ||
		!strings.HasPrefix(lines[2], consoleKey+" ") || !strings.HasSuffix(lines[2], consoleKeySuffix) {
		t.Errorf("authorized_keys should hold the customer's key, then the marked console key:\n%s", strings.Join(lines, "\n"))
	}
	if got := agent.exec(t, "stat", "--format=%a %U", authorizedKeysPath, "/root/.ssh"); got != "600 root\n700 root" {
		t.Errorf("modes and owners of authorized_keys and .ssh = %q, want 600 and 700, owned by root", got)
	}
}

// Every bad knock is sent before one good knock. The agent checks packets and
// fetches keys one at a time, in order, so a bad knock that got through would
// fetch a key first, and the good knock's fetch would follow within
// milliseconds. A healthy agent fetches exactly once however long the test
// waits, so the wait below can't fail it.
func TestKnocksThatFailVerificationAreIgnored(t *testing.T) {
	t.Parallel()
	cloud := newFakeCloud(t)
	agent := startAgent(t, cloud, agentOptions{})
	agent.addCustomerKey(t)
	agent.waitUntilReady(t, cloud)
	_, someoneElsesKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	valid := knock(cloud.knockKey, resourceID, time.Now())

	for name, packet := range map[string][]byte{
		"signed by another key":        knock(someoneElsesKey, resourceID, time.Now()),
		"signed for another VM":        knock(cloud.knockKey, "another-resource", time.Now()),
		"sent ten minutes ago":         knock(cloud.knockKey, resourceID, time.Now().Add(-10*time.Minute)),
		"sent ten minutes from now":    knock(cloud.knockKey, resourceID, time.Now().Add(10*time.Minute)),
		"cut short":                    valid[:len(valid)-1],
		"with a byte after the packet": append(append([]byte(nil), valid...), 0),
	} {
		t.Logf("sending a knock %s", name)
		agent.sendKnock(t, packet)
	}
	agent.sendKnock(t, valid)

	eventually(t, func() bool { _, keyRequests, _ := cloud.snapshot(); return len(keyRequests) > 0 },
		"the valid knock sent last never fetched a key")
	time.Sleep(time.Second)
	if _, keyRequests, _ := cloud.snapshot(); len(keyRequests) != 1 {
		t.Errorf("the agent fetched %d console keys, want 1: a knock that failed verification got through", len(keyRequests))
	}
}

// The key expires two seconds after the API hands it out, so the agent must
// have removed it a few seconds later however it schedules cleanup.
func TestTheConsoleKeyIsRemovedAndTheCustomersKeysStay(t *testing.T) {
	t.Parallel()
	cloud := newFakeCloud(t)
	cloud.keyExpires = 2 * time.Second
	agent := startAgent(t, cloud, agentOptions{cleanupInterval: time.Second})
	agent.addCustomerKey(t)
	agent.waitUntilReady(t, cloud)

	agent.sendKnock(t, knock(cloud.knockKey, resourceID, time.Now()))
	eventually(t, func() bool { _, keyRequests, _ := cloud.snapshot(); return len(keyRequests) > 0 },
		"the knock never fetched a key")

	eventually(t, func() bool { return agent.authorizedKeys(t) == customerKey },
		"authorized_keys should be back to only the customer's key, but holds:\n"+agent.authorizedKeys(t))
}

func TestAgentStopsCleanlyOnSIGTERM(t *testing.T) {
	t.Parallel()
	cloud := newFakeCloud(t)
	agent := startAgent(t, cloud, agentOptions{})
	agent.waitUntilReady(t, cloud)

	// docker stop sends SIGTERM, then SIGKILL after the grace period.
	if output, err := exec.Command("docker", "stop", "--time", "10", agent.name).CombinedOutput(); err != nil {
		t.Fatalf("docker stop: %v\n%s", err, output)
	}

	if got := agent.exitCode(t); got != "0" {
		t.Errorf("exit code after SIGTERM = %s, want 0", got)
	}
}

func TestAgentRefusesToStartWithoutAResourceID(t *testing.T) {
	t.Parallel()
	cloud := newFakeCloud(t)
	agent := startAgent(t, cloud, agentOptions{withoutResourceID: true})

	if got := agent.exitCode(t); got != "1" {
		t.Errorf("exit code without a resource ID = %s, want 1", got)
	}
	if metadataRequests, _, _ := cloud.snapshot(); len(metadataRequests) != 0 {
		t.Errorf("the agent called the metadata service %d times without a resource ID", len(metadataRequests))
	}
}

func TestVersionFlagPrintsTheBuildVersion(t *testing.T) {
	t.Parallel()
	output, err := exec.Command("docker", "run", "--rm", "--volume", agentBinaryDirectory+":/agent:ro",
		image, "/agent/sc-console-agent", "-v").CombinedOutput()
	if err != nil {
		t.Fatalf("-v: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), agentVersion) {
		t.Errorf("-v printed %q, want it to include %s", output, agentVersion)
	}
}
