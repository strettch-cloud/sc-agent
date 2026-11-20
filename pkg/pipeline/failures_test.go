package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/strettch-cloud/sc-agent/pkg/clients/metadata"
	"github.com/strettch-cloud/sc-agent/pkg/config"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// newObservedProcessor builds a processor whose metadata service is
// metadataService, and returns what it logs. Its key manager points at the
// home of the user running the tests, so every test here must fail before a
// key would be installed.
func newObservedProcessor(t *testing.T, metadataService http.HandlerFunc) (*Processor, *observer.ObservedLogs) {
	t.Helper()
	server := httptest.NewServer(metadataService)
	t.Cleanup(server.Close)
	core, logs := observer.New(zap.DebugLevel)
	processor, err := NewProcessor(&config.Config{
		MetadataServiceEndpoint: server.URL,
		ResourceID:              "resource",
		Console:                 config.ConsoleConfig{MaxPacketSize: 1024, SignatureTimeout: 30},
		HTTPTimeout:             time.Second,
		KeyCleanupInterval:      time.Hour,
		HeartbeatInterval:       time.Hour,
		MaxRetries:              1,
	}, zap.New(core))
	if err != nil {
		t.Fatal(err)
	}
	return processor, logs
}

// attemptsBeforeGivingUp is how many times in a row the agent asks the
// metadata service before a startup or a knock fails.
const attemptsBeforeGivingUp = 10

// unavailableMetadataService answers every request with 503 and counts them.
// Retry-After 0 makes the agent ask again at once instead of after 10 s.
func unavailableMetadataService() (http.HandlerFunc, *atomic.Int32) {
	var requests atomic.Int32
	return func(response http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		response.Header().Set(metadata.HeaderRetryAfter, "0")
		http.Error(response, "unavailable", http.StatusServiceUnavailable)
	}, &requests
}

func unavailable(response http.ResponseWriter, _ *http.Request) {
	http.Error(response, "unavailable", http.StatusServiceUnavailable)
}

func TestAHeartbeatWithoutMetadataIsSkippedAndReported(t *testing.T) {
	metadataService, requests := unavailableMetadataService()
	processor, logs := newObservedProcessor(t, metadataService)

	processor.sendHeartbeat()

	if logs.FilterMessage("Failed to get metadata for heartbeat").Len() != 1 {
		t.Errorf("the agent didn't report the skipped heartbeat; it logged %v", logs.All())
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("the heartbeat asked the metadata service %d times, want 1", got)
	}
}

func TestAKnockWhoseTokenFetchFails10TimesInARowStopsBeforeTheKey(t *testing.T) {
	metadataService, requests := unavailableMetadataService()
	processor, _ := newObservedProcessor(t, metadataService)

	err := processor.processKnock(context.Background(), processor.logger)

	if err == nil || !strings.Contains(err.Error(), "failed to get metadata") {
		t.Errorf("processKnock error = %v, want one about the metadata", err)
	}
	if got := requests.Load(); got != attemptsBeforeGivingUp {
		t.Errorf("the knock asked the metadata service %d times, want %d", got, attemptsBeforeGivingUp)
	}
}

func TestTheAgentFailsToStartAfter10FailedTokenAttemptsInARow(t *testing.T) {
	metadataService, requests := unavailableMetadataService()
	processor, _ := newObservedProcessor(t, metadataService)
	t.Cleanup(processor.Stop)

	err := processor.Start()

	if err == nil || !strings.Contains(err.Error(), "failed to fetch metadata") {
		t.Errorf("Start error = %v, want one about the metadata", err)
	}
	if got := requests.Load(); got != attemptsBeforeGivingUp {
		t.Errorf("the agent asked the metadata service %d times, want %d", got, attemptsBeforeGivingUp)
	}
}

// A knock key the agent can't use is reported and the knock goes on with the
// token; here the cloud API is down too, so it stops before the key.
func TestAKnockKeyTheAgentCantUseIsReported(t *testing.T) {
	cloudAPI := httptest.NewServer(http.HandlerFunc(unavailable))
	t.Cleanup(cloudAPI.Close)
	processor, logs := newObservedProcessor(t, func(response http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(response).Encode(map[string]string{
			"token": "token", "console-public-key": "not-a-key", "api-url": cloudAPI.URL,
			"expires-at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		})
	})

	err := processor.processKnock(context.Background(), processor.logger)

	if logs.FilterMessage("Failed to update public key").Len() != 1 {
		t.Errorf("the agent didn't report the knock key it can't use; it logged %v", logs.All())
	}
	if err == nil || !strings.Contains(err.Error(), "failed to fetch console key") {
		t.Errorf("processKnock error = %v, want one about the console key", err)
	}
}
