package metadata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
)

const (
	issuedToken      = "token-for-this-vm"
	issuedKnockKey   = "5dd126184f785631d3e3afcddab5a77dfe2d3a2bed65e75d89cca0d00a03714d"
	issuedAPIAddress = "https://api.test.invalid"
	tokenAnswer      = `{"token":"` + issuedToken + `","publicKey":"` + issuedKnockKey +
		`","cloudAPIUrl":"` + issuedAPIAddress + `"}`
)

// tokenService stands in for the metadata service at 169.254.169.254. It
// answers each request with the next of its answers, and repeats the last.
type tokenService struct {
	mutex    sync.Mutex
	answers  []tokenServiceAnswer
	requests []http.Header
}

type tokenServiceAnswer struct {
	status int
	body   string
}

func newTokenService(t *testing.T, answers ...tokenServiceAnswer) (*tokenService, string) {
	t.Helper()
	service := &tokenService{answers: answers}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		service.mutex.Lock()
		defer service.mutex.Unlock()
		service.requests = append(service.requests, request.Header.Clone())
		answer := service.answers[0]
		if len(service.answers) > 1 {
			service.answers = service.answers[1:]
		}
		response.WriteHeader(answer.status)
		_, _ = response.Write([]byte(answer.body))
	}))
	t.Cleanup(server.Close)
	return service, server.URL
}

func (service *tokenService) requestHeaders() []http.Header {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	return append([]http.Header(nil), service.requests...)
}

func TestTheAgentAsksTheMetadataServiceForItsToken(t *testing.T) {
	t.Run("the request carries Accept application/json and Metadata-Flavor StrettchCloud, and no X-Resource-ID", func(t *testing.T) {
		service, address := newTokenService(t, tokenServiceAnswer{http.StatusOK, tokenAnswer})
		client := NewClient(address, time.Second, zap.NewNop())

		_, _ = client.GetMetadata(context.Background())

		requests := service.requestHeaders()
		if len(requests) != 1 {
			t.Fatalf("the agent sent %d requests, want 1", len(requests))
		}
		if got := requests[0].Values("Accept"); len(got) != 1 || got[0] != "application/json" {
			t.Errorf("Accept = %q, want exactly [application/json]", got)
		}
		if got := requests[0].Values("Metadata-Flavor"); len(got) != 1 || got[0] != "StrettchCloud" {
			t.Errorf("Metadata-Flavor = %q, want exactly [StrettchCloud]", got)
		}
		if got := requests[0].Values("X-Resource-ID"); len(got) != 0 {
			t.Errorf("the request carries X-Resource-ID %q, want no such header", got)
		}
	})

	t.Run("a 200 with a token gives the agent the token, the knock key and the API address", func(t *testing.T) {
		_, address := newTokenService(t, tokenServiceAnswer{http.StatusOK, tokenAnswer})
		client := NewClient(address, time.Second, zap.NewNop())

		info, err := client.GetMetadata(context.Background())

		if err != nil {
			t.Fatalf("GetMetadata error = %v, want none", err)
		}
		want := Info{Token: issuedToken, PublicKey: issuedKnockKey, CloudAPIURL: issuedAPIAddress}
		if *info != want {
			t.Errorf("the agent kept %+v, want %+v", *info, want)
		}
	})

	t.Run("the agent keeps the token and does not ask again for the next request", func(t *testing.T) {
		service, address := newTokenService(t, tokenServiceAnswer{http.StatusOK, tokenAnswer})
		client := NewClient(address, time.Second, zap.NewNop())
		_, _ = client.GetMetadata(context.Background())

		info, err := client.GetMetadata(context.Background())

		if err != nil || info.Token != issuedToken {
			t.Errorf("second GetMetadata = %+v, %v, want the kept token %q", info, err, issuedToken)
		}
		if got := len(service.requestHeaders()); got != 1 {
			t.Errorf("the agent asked the metadata service %d times, want 1", got)
		}
	})

	for name, answer := range map[string]tokenServiceAnswer{
		"a 403":                            {http.StatusForbidden, "METADATA_FLAVOR_REQUIRED"},
		"a 404":                            {http.StatusNotFound, "not found"},
		"a 503":                            {http.StatusServiceUnavailable, `{"code":"NOT_READY"}`},
		"a 500":                            {http.StatusInternalServerError, `{"code":"INTERNAL_ERROR"}`},
		"a 203 whose body carries a token": {http.StatusNonAuthoritativeInfo, tokenAnswer},
		"a 500 whose body carries a token": {http.StatusInternalServerError, tokenAnswer},
		"a 200 with no token":              {http.StatusOK, `{"cloudAPIUrl":"` + issuedAPIAddress + `"}`},
		"a 200 whose body is not JSON":     {http.StatusOK, "<html>"},
	} {
		t.Run(name+" gives the agent no token", func(t *testing.T) {
			_, address := newTokenService(t, answer)
			client := NewClient(address, time.Second, zap.NewNop())

			info, err := client.GetMetadata(context.Background())

			if err == nil {
				t.Errorf("GetMetadata = %+v, want an error and no token", info)
			}
			if info != nil {
				t.Errorf("GetMetadata returned %+v, want nothing", info)
			}
		})
	}

	t.Run("the agent asks again after an answer with no token, and keeps the token the next 200 brings", func(t *testing.T) {
		service, address := newTokenService(t,
			tokenServiceAnswer{http.StatusNotFound, "not found"},
			tokenServiceAnswer{http.StatusOK, tokenAnswer})
		client := NewClient(address, time.Second, zap.NewNop())
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		info, err := client.GetMetadataForStartup(ctx)

		if err != nil || info.Token != issuedToken {
			t.Errorf("GetMetadataForStartup = %+v, %v, want the token %q", info, err, issuedToken)
		}
		if got := len(service.requestHeaders()); got != 2 {
			t.Errorf("the agent asked the metadata service %d times, want 2: one refused, one answered", got)
		}
	})
}
