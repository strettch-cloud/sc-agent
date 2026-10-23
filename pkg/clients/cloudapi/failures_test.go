package cloudapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strettch-cloud/sc-agent/pkg/clients/metadata"
	"go.uber.org/zap"
)

// newMetadataClient talks to a fake metadata service that names cloudAPIURL
// as the cloud API, after answering its first failures requests with 503.
func newMetadataClient(t *testing.T, cloudAPIURL string, failures int) *metadata.Client {
	t.Helper()
	var mutex sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		mutex.Lock()
		defer mutex.Unlock()
		if failures > 0 {
			failures--
			http.Error(response, "unavailable", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(response).Encode(metadata.Response{Token: "token", CloudAPIUrl: cloudAPIURL})
	}))
	t.Cleanup(server.Close)
	return metadata.NewClient(server.URL, time.Second, zap.NewNop())
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

// The cloud API address comes from the metadata service, so a request retries
// a failed metadata call instead of failing the knock at once. Two failures:
// the first is the client's plain call, the second its first retry.
func TestARequestRetriesTheMetadataService(t *testing.T) {
	t.Parallel()
	cloudAPI := newCloudAPI(t, func(response http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(response).Encode(ConsoleKeyResponse{Data: ConsoleKey{PublicKey: "ssh-ed25519 AAAA console"}})
	})
	client := NewClient(newMetadataClient(t, cloudAPI, 2), "resource", time.Second, zap.NewNop())

	key, err := client.FetchConsoleKey(context.Background(), "token")

	if err != nil || key.PublicKey != "ssh-ed25519 AAAA console" {
		t.Errorf("FetchConsoleKey = %+v, %v, want the key after retrying", key, err)
	}
}

func TestRequestsFailWhileTheMetadataServiceStaysDown(t *testing.T) {
	t.Parallel()
	for name, request := range map[string]func(*Client, context.Context) error{
		"console key": func(client *Client, ctx context.Context) error {
			_, err := client.FetchConsoleKey(ctx, "token")
			return err
		},
		"heartbeat": func(client *Client, ctx context.Context) error { return client.SendHeartbeat(ctx, "token", "1.0.0") },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			client := NewClient(newMetadataClient(t, "http://unused.invalid", 1000), "resource", time.Second, zap.NewNop())
			// Long enough for one retry, short of waiting for all five.
			ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
			defer cancel()

			assertErrorContains(t, request(client, ctx), "failed to get cloud API endpoint")
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
			client := NewClient(newMetadataClient(t, newCloudAPI(t, testCase.respond), 0), "resource", time.Second, zap.NewNop())

			_, err := client.FetchConsoleKey(context.Background(), "token")

			assertErrorContains(t, err, testCase.want)
		})
	}
}

// A cloud API address the agent can't parse fails each request with an
// error rather than a crash.
func TestAMalformedCloudAPIAddressFailsRequests(t *testing.T) {
	t.Parallel()
	client := NewClient(newMetadataClient(t, "http://[::1", 0), "resource", time.Second, zap.NewNop())

	_, err := client.FetchConsoleKey(context.Background(), "token")
	assertErrorContains(t, err, "failed to create request")
	assertErrorContains(t, client.SendHeartbeat(context.Background(), "token", "1.0.0"), "failed to create request")
}

func TestAHeartbeatToAnUnreachableCloudAPIFails(t *testing.T) {
	t.Parallel()
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	client := NewClient(newMetadataClient(t, closed.URL, 0), "resource", time.Second, zap.NewNop())

	assertErrorContains(t, client.SendHeartbeat(context.Background(), "token", "1.0.0"), "request failed")
}
