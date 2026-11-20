package cloudapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strettch-cloud/sc-agent/pkg/clients/metadata"
	"go.uber.org/zap"
)

// fakeMetadataService names cloudAPIURL as the cloud API, after answering its
// first failures requests with 503. Each 503 carries Retry-After 0, so the
// agent asks again at once instead of after 10 s.
type fakeMetadataService struct {
	mutex    sync.Mutex
	failures int
	requests int
}

func (service *fakeMetadataService) received() int {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	return service.requests
}

func newMetadataService(t *testing.T, cloudAPIURL string, failures int) (*fakeMetadataService, *metadata.Client) {
	t.Helper()
	service := &fakeMetadataService{failures: failures}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		service.mutex.Lock()
		defer service.mutex.Unlock()
		service.requests++
		if service.failures > 0 {
			service.failures--
			response.Header().Set(metadata.HeaderRetryAfter, "0")
			http.Error(response, "unavailable", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(response).Encode(metadata.Response{
			Token:       "token",
			ExpiresAt:   time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			CloudAPIUrl: cloudAPIURL,
			PublicKey:   "knock-key",
		})
	}))
	t.Cleanup(server.Close)
	return service, metadata.NewClient(server.URL, time.Second, zap.NewNop())
}

func newMetadataClient(t *testing.T, cloudAPIURL string, failures int) *metadata.Client {
	t.Helper()
	_, client := newMetadataService(t, cloudAPIURL, failures)
	return client
}

func newCloudAPI(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server.URL
}

func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("error = %v, want one containing %q", err, want)
	}
}

// The console-key client looks up the cloud API address with the token, and
// that lookup makes up to 10 attempts.
func TestTheConsoleKeyClientAsksTheMetadataServiceUpTo10Times(t *testing.T) {
	t.Parallel()
	const attemptsBeforeGivingUp = 10
	consoleKeyAPI := func(response http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(response).Encode(ConsoleKeyResponse{Data: ConsoleKey{PublicKey: "ssh-ed25519 AAAA console"}})
	}

	t.Run("after 9 failed answers the 10th gives the API address, and the console key is fetched", func(t *testing.T) {
		t.Parallel()
		service, metadataClient := newMetadataService(t, newCloudAPI(t, consoleKeyAPI), attemptsBeforeGivingUp-1)
		client := NewClient(metadataClient, time.Second, zap.NewNop())

		key, err := client.FetchConsoleKey(context.Background(), "token")

		if err != nil || key.PublicKey != "ssh-ed25519 AAAA console" {
			t.Errorf("FetchConsoleKey = %+v, %v, want the console key", key, err)
		}
		if got := service.received(); got != attemptsBeforeGivingUp {
			t.Errorf("the agent asked the metadata service %d times, want %d", got, attemptsBeforeGivingUp)
		}
	})

	for name, request := range map[string]func(*Client, context.Context) error{
		"the console key": func(client *Client, ctx context.Context) error {
			_, err := client.FetchConsoleKey(ctx, "token")
			return err
		},
		"a heartbeat": func(client *Client, ctx context.Context) error { return client.SendHeartbeat(ctx, "token", "1.0.0") },
	} {
		t.Run("after 10 failed answers the request for "+name+" fails and the cloud API is never asked", func(t *testing.T) {
			t.Parallel()
			var cloudAPIRequests atomic.Int32
			cloudAPI := newCloudAPI(t, func(response http.ResponseWriter, request *http.Request) {
				cloudAPIRequests.Add(1)
				consoleKeyAPI(response, request)
			})
			service, metadataClient := newMetadataService(t, cloudAPI, attemptsBeforeGivingUp)
			client := NewClient(metadataClient, time.Second, zap.NewNop())

			err := request(client, context.Background())

			assertErrorContains(t, err, "failed to get cloud API endpoint")
			if got := service.received(); got != attemptsBeforeGivingUp {
				t.Errorf("the agent asked the metadata service %d times, want %d", got, attemptsBeforeGivingUp)
			}
			if got := cloudAPIRequests.Load(); got != 0 {
				t.Errorf("the cloud API got %d requests, want none", got)
			}
		})
	}
}

func TestAConsoleKeyResponseTheAgentCantUseIsAnError(t *testing.T) {
	t.Parallel()
	for name, testCase := range map[string]struct {
		respond http.HandlerFunc
		want    string
	}{
		"cut short": {func(response http.ResponseWriter, _ *http.Request) {
			response.Header().Set("Content-Length", "100")
			_, _ = response.Write([]byte("{"))
		}, "failed to read response"},
		"not JSON": {func(response http.ResponseWriter, _ *http.Request) {
			_, _ = response.Write([]byte("<html>"))
		}, "failed to parse response"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			client := NewClient(newMetadataClient(t, newCloudAPI(t, testCase.respond), 0), time.Second, zap.NewNop())

			_, err := client.FetchConsoleKey(context.Background(), "token")

			assertErrorContains(t, err, testCase.want)
		})
	}
}

// A cloud API address the agent can't parse fails each request with an
// error rather than a crash.
func TestAMalformedCloudAPIAddressFailsRequests(t *testing.T) {
	t.Parallel()
	client := NewClient(newMetadataClient(t, "http://[::1", 0), time.Second, zap.NewNop())

	_, err := client.FetchConsoleKey(context.Background(), "token")
	assertErrorContains(t, err, "failed to create request")
	assertErrorContains(t, client.SendHeartbeat(context.Background(), "token", "1.0.0"), "failed to create request")
}

func TestAHeartbeatToAnUnreachableCloudAPIFails(t *testing.T) {
	t.Parallel()
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	client := NewClient(newMetadataClient(t, closed.URL, 0), time.Second, zap.NewNop())

	assertErrorContains(t, client.SendHeartbeat(context.Background(), "token", "1.0.0"), "request failed")
}
