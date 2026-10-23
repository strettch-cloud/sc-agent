package metadata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestMetadataTheAgentCantUseIsAnError(t *testing.T) {
	t.Parallel()
	cutShort := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Length", "100")
		_, _ = response.Write([]byte("{"))
	}))
	defer cutShort.Close()

	for name, testCase := range map[string]struct{ endpoint, want string }{
		"a response cut short": {cutShort.URL, "failed to read response"},
		// The address comes from agent.yaml, so a typo there must fail
		// cleanly.
		"a malformed address": {"http://[::1", "failed to create request"},
	} {
		t.Run(name, func(t *testing.T) {
			client := NewClient(testCase.endpoint, time.Second, zap.NewNop())

			_, err := client.GetMetadata(context.Background(), "resource")

			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Errorf("GetMetadata error = %v, want one containing %q", err, testCase.want)
			}
		})
	}
}
