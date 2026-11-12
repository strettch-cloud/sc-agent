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
		"a malformed address":  {"http://[::1", "failed to create request"},
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

// The metadata service at 169.254.169.254 answers a request without this header
// with 403 METADATA_FLAVOR_REQUIRED.
func TestTheTokenRequestSendsTheMetadataFlavorHeader(t *testing.T) {
	t.Parallel()
	metadataService := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Metadata-Flavor") != "StrettchCloud" {
			http.Error(response, "METADATA_FLAVOR_REQUIRED", http.StatusForbidden)
			return
		}
		_, _ = response.Write([]byte(`{"token":"token","cloudAPIUrl":"https://api.test.invalid"}`))
	}))
	defer metadataService.Close()
	client := NewClient(metadataService.URL, time.Second, zap.NewNop())

	info, err := client.GetMetadata(context.Background(), "resource")

	if err != nil {
		t.Fatalf("GetMetadata error = %v, want none", err)
	}
	if info.Token != "token" {
		t.Errorf("token = %q, want %q", info.Token, "token")
	}
}
