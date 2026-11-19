package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	retry "github.com/strettch-cloud/sc-agent/pkg/connectionretry"
	"go.uber.org/zap"
)

const (
	issuedToken      = "token-for-this-vm"
	renewedToken     = "renewed-token-for-this-vm"
	issuedKnockKey   = "5dd126184f785631d3e3afcddab5a77dfe2d3a2bed65e75d89cca0d00a03714d"
	issuedAPIAddress = "https://api.test.invalid"
	// oldPath is where the metadata service used to hand out the token.
	oldPath = "/metadata/v1/auth-token"
	// retryDelayWithoutRetryAfter is how long the agent waits before asking
	// again when the answer asked for no wait it can read.
	retryDelayWithoutRetryAfter = 10 * time.Second
	attemptsBeforeGivingUp      = 10
)

// startOfTest is when every test's clock starts, a whole second so an HTTP
// date can name it exactly.
var startOfTest = time.Date(2030, time.January, 1, 0, 0, 0, 0, time.UTC)

// completeAnswer is a 200 body with all four values, for a token that expires
// at expiresAt.
func completeAnswer(token string, expiresAt time.Time) string {
	body, _ := json.Marshal(map[string]string{
		"token":              token,
		"expires-at":         expiresAt.UTC().Format(time.RFC3339),
		"api-url":            issuedAPIAddress,
		"console-public-key": issuedKnockKey,
	})
	return string(body)
}

// answerWithout is a complete answer for a token with an hour left, minus one
// value.
func answerWithout(value string) string {
	answer := map[string]string{
		"token":              issuedToken,
		"expires-at":         startOfTest.Add(time.Hour).Format(time.RFC3339),
		"api-url":            issuedAPIAddress,
		"console-public-key": issuedKnockKey,
	}
	delete(answer, value)
	body, _ := json.Marshal(answer)
	return string(body)
}

// tokenService stands in for the metadata service at 169.254.169.254. It
// answers each request to /v1/token with the next of its answers, and repeats
// the last; any other path gets a 404.
type tokenService struct {
	mutex    sync.Mutex
	answers  []tokenServiceAnswer
	requests []*http.Request
}

type tokenServiceAnswer struct {
	status int
	body   string
	// retryAfter is the answer's Retry-After header. Empty leaves it out.
	retryAfter string
}

func ok(body string) tokenServiceAnswer { return tokenServiceAnswer{status: http.StatusOK, body: body} }

var unavailable = tokenServiceAnswer{status: http.StatusServiceUnavailable, body: `{"code":"NOT_READY"}`}

// newTokenService starts the fake and returns it with the address of its
// /v1/token.
func newTokenService(t *testing.T, answers ...tokenServiceAnswer) (*tokenService, string) {
	t.Helper()
	service := &tokenService{answers: answers}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		service.mutex.Lock()
		defer service.mutex.Unlock()
		service.requests = append(service.requests, request.Clone(context.Background()))
		if request.URL.Path != "/v1/token" {
			http.NotFound(response, request)
			return
		}
		answer := service.answers[0]
		if len(service.answers) > 1 {
			service.answers = service.answers[1:]
		}
		if answer.retryAfter != "" {
			response.Header().Set("Retry-After", answer.retryAfter)
		}
		response.WriteHeader(answer.status)
		_, _ = response.Write([]byte(answer.body))
	}))
	t.Cleanup(server.Close)
	return service, server.URL + "/v1/token"
}

func (service *tokenService) received() []*http.Request {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	return append([]*http.Request(nil), service.requests...)
}

// fakeClock is the agent's clock in these tests. A wait between attempts is
// recorded and moves the clock on at once.
type fakeClock struct {
	mutex sync.Mutex
	now   time.Time
	waits []time.Duration
}

func (clock *fakeClock) Now() time.Time {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	return clock.now
}

func (clock *fakeClock) Wait(_ context.Context, d time.Duration) error {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	clock.waits = append(clock.waits, d)
	clock.now = clock.now.Add(d)
	return nil
}

func (clock *fakeClock) pass(d time.Duration) {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	clock.now = clock.now.Add(d)
}

func (clock *fakeClock) waited() []time.Duration {
	clock.mutex.Lock()
	defer clock.mutex.Unlock()
	return append([]time.Duration(nil), clock.waits...)
}

// newAgentClient is the agent's metadata client on a clock that starts at
// startOfTest.
func newAgentClient(address string) (*Client, *fakeClock) {
	clock := &fakeClock{now: startOfTest}
	client := NewClient(address, time.Second, zap.NewNop())
	client.now = clock.Now
	client.wait = clock.Wait
	return client, clock
}

// unreachableAddress is a /v1/token nothing answers on.
func unreachableAddress(t *testing.T) string {
	t.Helper()
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	return closed.URL + "/v1/token"
}

func TestTheAgentGetsItsTokenAPIAddressAndConsoleKey(t *testing.T) {
	complete := completeAnswer(issuedToken, startOfTest.Add(time.Hour))

	t.Run("the agent asks /v1/token with GET, Accept application/json and Metadata-Flavor StrettchCloud, with no query and no X-Resource-ID", func(t *testing.T) {
		service, address := newTokenService(t, ok(complete))
		client, _ := newAgentClient(address)

		_, _ = client.GetMetadata(context.Background())

		requests := service.received()
		if len(requests) != 1 {
			t.Fatalf("the agent sent %d requests, want 1", len(requests))
		}
		request := requests[0]
		if request.Method != http.MethodGet || request.URL.Path != "/v1/token" || request.URL.RawQuery != "" {
			t.Errorf("the agent asked %s %s?%s, want GET /v1/token with no query", request.Method, request.URL.Path, request.URL.RawQuery)
		}
		if got := request.Header.Values("Accept"); len(got) != 1 || got[0] != "application/json" {
			t.Errorf("Accept = %q, want exactly [application/json]", got)
		}
		if got := request.Header.Values("Metadata-Flavor"); len(got) != 1 || got[0] != "StrettchCloud" {
			t.Errorf("Metadata-Flavor = %q, want exactly [StrettchCloud]", got)
		}
		if got := request.Header.Values("X-Resource-ID"); len(got) != 0 {
			t.Errorf("the request carries X-Resource-ID %q, want no such header", got)
		}
	})

	t.Run("a 200 with the token, its expiry, the API address and the console key gives the agent all four", func(t *testing.T) {
		expiresAt := startOfTest.Add(time.Hour)
		_, address := newTokenService(t, ok(completeAnswer(issuedToken, expiresAt)))
		client, _ := newAgentClient(address)

		info, err := client.GetMetadata(context.Background())

		if err != nil {
			t.Fatalf("GetMetadata error = %v, want none", err)
		}
		want := Info{Token: issuedToken, PublicKey: issuedKnockKey, CloudAPIURL: issuedAPIAddress, ExpiresAt: expiresAt}
		if *info != want {
			t.Errorf("the agent got %+v, want %+v", *info, want)
		}
	})

	for _, row := range []struct{ timeLeft, heldFor time.Duration }{
		{60 * time.Minute, 30 * time.Minute},
		{31 * time.Minute, 30 * time.Minute},
		{30 * time.Minute, 29 * time.Minute},
		{10 * time.Minute, 9 * time.Minute},
		{61 * time.Second, time.Second},
	} {
		t.Run("with "+row.timeLeft.String()+" left on the token the agent uses the held values for "+row.heldFor.String()+" without asking again", func(t *testing.T) {
			service, address := newTokenService(t, ok(completeAnswer(issuedToken, startOfTest.Add(row.timeLeft))))
			client, clock := newAgentClient(address)
			_, _ = client.GetMetadata(context.Background())
			clock.pass(row.heldFor - time.Nanosecond)

			info, err := client.GetMetadata(context.Background())

			if err != nil || info.Token != issuedToken {
				t.Errorf("GetMetadata = %+v, %v, want the held token %q", info, err, issuedToken)
			}
			if got := len(service.received()); got != 1 {
				t.Errorf("the agent asked the metadata service %d times, want 1", got)
			}
		})

		t.Run("with "+row.timeLeft.String()+" left on the token the agent asks again once "+row.heldFor.String()+" have passed, and uses the new answer", func(t *testing.T) {
			service, address := newTokenService(t,
				ok(completeAnswer(issuedToken, startOfTest.Add(row.timeLeft))),
				ok(completeAnswer(renewedToken, startOfTest.Add(row.heldFor+time.Hour))))
			client, clock := newAgentClient(address)
			_, _ = client.GetMetadata(context.Background())
			clock.pass(row.heldFor)

			info, err := client.GetMetadata(context.Background())

			if err != nil || info.Token != renewedToken {
				t.Errorf("GetMetadata = %+v, %v, want the renewed token %q", info, err, renewedToken)
			}
			if got := len(service.received()); got != 2 {
				t.Errorf("the agent asked the metadata service %d times, want 2", got)
			}
		})
	}

	t.Run("a knock or a startup uses the held values without asking again", func(t *testing.T) {
		service, address := newTokenService(t, ok(complete))
		client, _ := newAgentClient(address)
		_, _ = client.GetMetadata(context.Background())

		info, err := client.GetMetadataWithRetry(context.Background())

		if err != nil || info.Token != issuedToken {
			t.Errorf("GetMetadataWithRetry = %+v, %v, want the held token %q", info, err, issuedToken)
		}
		if got := len(service.received()); got != 1 {
			t.Errorf("the agent asked the metadata service %d times, want 1", got)
		}
	})

	for _, timeLeft := range []time.Duration{time.Minute, 59 * time.Second, 0, -time.Hour} {
		t.Run("a token with "+timeLeft.String()+" left is not held, and the attempt fails", func(t *testing.T) {
			service, address := newTokenService(t,
				ok(completeAnswer(issuedToken, startOfTest.Add(timeLeft))),
				ok(completeAnswer(renewedToken, startOfTest.Add(time.Hour))))
			client, _ := newAgentClient(address)

			info, err := client.GetMetadata(context.Background())

			if err == nil || info != nil {
				t.Errorf("GetMetadata = %+v, %v, want an error and nothing", info, err)
			}
			next, err := client.GetMetadata(context.Background())
			if err != nil || next.Token != renewedToken {
				t.Errorf("the next GetMetadata = %+v, %v, want the token %q from a new request", next, err, renewedToken)
			}
			if got := len(service.received()); got != 2 {
				t.Errorf("the agent asked the metadata service %d times, want 2", got)
			}
		})
	}

	for name, answer := range map[string]tokenServiceAnswer{
		"a 403":                             {status: http.StatusForbidden, body: "METADATA_FLAVOR_REQUIRED"},
		"a 404":                             {status: http.StatusNotFound, body: "not found"},
		"a 503":                             unavailable,
		"a 500 whose body carries all four": {status: http.StatusInternalServerError, body: complete},
		"a 201 whose body carries all four": {status: http.StatusCreated, body: complete},
		"a 200 with no token":               ok(answerWithout("token")),
		"a 200 with no expiry":              ok(answerWithout("expires-at")),
		"a 200 with no API address":         ok(answerWithout("api-url")),
		"a 200 with no console key":         ok(answerWithout("console-public-key")),
		"a 200 whose expiry is not a time": ok(`{"token":"` + issuedToken + `","expires-at":"in an hour","api-url":"` +
			issuedAPIAddress + `","console-public-key":"` + issuedKnockKey + `"}`),
		"a 200 whose expiry has no time zone": ok(`{"token":"` + issuedToken + `","expires-at":"2030-01-01T01:00:00","api-url":"` +
			issuedAPIAddress + `","console-public-key":"` + issuedKnockKey + `"}`),
		"a 200 whose four values are empty": ok(`{"token":"","expires-at":"","api-url":"","console-public-key":""}`),
		"a 200 in the old shape": ok(`{"token":"` + issuedToken + `","cloudAPIUrl":"` + issuedAPIAddress +
			`","publicKey":"` + issuedKnockKey + `"}`),
		"a 200 whose body is not JSON": ok("<html>"),
	} {
		t.Run(name+" fails the attempt, and the agent holds nothing and asks again next time", func(t *testing.T) {
			service, address := newTokenService(t, answer, ok(complete))
			client, _ := newAgentClient(address)

			info, err := client.GetMetadata(context.Background())

			if err == nil || info != nil {
				t.Errorf("GetMetadata = %+v, %v, want an error and nothing", info, err)
			}
			next, err := client.GetMetadata(context.Background())
			if err != nil || next.Token != issuedToken {
				t.Errorf("the next GetMetadata = %+v, %v, want the token %q from a new request", next, err, issuedToken)
			}
			if got := len(service.received()); got != 2 {
				t.Errorf("the agent asked the metadata service %d times, want 2", got)
			}
		})
	}

	t.Run("no answer at all fails the attempt", func(t *testing.T) {
		client, _ := newAgentClient(unreachableAddress(t))

		info, err := client.GetMetadata(context.Background())

		if err == nil || info != nil {
			t.Errorf("GetMetadata = %+v, %v, want an error and nothing", info, err)
		}
	})

	t.Run("the heartbeat's fetch asks once and fails when that answer fails", func(t *testing.T) {
		service, address := newTokenService(t, unavailable, ok(complete))
		client, clock := newAgentClient(address)

		_, err := client.GetMetadata(context.Background())

		if err == nil {
			t.Error("GetMetadata error = nil, want the failed attempt")
		}
		if got := len(service.received()); got != 1 {
			t.Errorf("the agent asked the metadata service %d times, want 1", got)
		}
		if waits := clock.waited(); len(waits) != 0 {
			t.Errorf("the agent waited %v, want no wait", waits)
		}
	})

	t.Run("after a failed attempt the agent asks again, and uses the next complete answer", func(t *testing.T) {
		service, address := newTokenService(t, unavailable, ok(answerWithout("console-public-key")), ok(complete))
		client, _ := newAgentClient(address)

		info, err := client.GetMetadataWithRetry(context.Background())

		if err != nil || info.Token != issuedToken || info.PublicKey != issuedKnockKey || info.CloudAPIURL != issuedAPIAddress {
			t.Errorf("GetMetadataWithRetry = %+v, %v, want the complete answer", info, err)
		}
		if got := len(service.received()); got != 3 {
			t.Errorf("the agent asked the metadata service %d times, want 3", got)
		}
	})

	t.Run("a complete answer on the 10th attempt is used", func(t *testing.T) {
		answers := make([]tokenServiceAnswer, 0, attemptsBeforeGivingUp)
		for range attemptsBeforeGivingUp - 1 {
			answers = append(answers, unavailable)
		}
		service, address := newTokenService(t, append(answers, ok(complete))...)
		client, _ := newAgentClient(address)

		info, err := client.GetMetadataWithRetry(context.Background())

		if err != nil || info.Token != issuedToken {
			t.Errorf("GetMetadataWithRetry = %+v, %v, want the token %q", info, err, issuedToken)
		}
		if got := len(service.received()); got != attemptsBeforeGivingUp {
			t.Errorf("the agent asked the metadata service %d times, want %d", got, attemptsBeforeGivingUp)
		}
	})

	t.Run("after its 10th failed attempt in a row the agent gives up without waiting again, and holds nothing", func(t *testing.T) {
		answers := make([]tokenServiceAnswer, 0, attemptsBeforeGivingUp+1)
		for range attemptsBeforeGivingUp {
			answers = append(answers, unavailable)
		}
		service, address := newTokenService(t, append(answers, ok(complete))...)
		client, clock := newAgentClient(address)

		info, err := client.GetMetadataWithRetry(context.Background())

		if !errors.Is(err, retry.ErrMaxRetriesExceeded) || info != nil {
			t.Errorf("GetMetadataWithRetry = %+v, %v, want nothing and %v", info, err, retry.ErrMaxRetriesExceeded)
		}
		if got := len(service.received()); got != attemptsBeforeGivingUp {
			t.Errorf("the agent asked the metadata service %d times, want %d", got, attemptsBeforeGivingUp)
		}
		if got := len(clock.waited()); got != attemptsBeforeGivingUp-1 {
			t.Errorf("the agent waited %d times, want %d: once between each two attempts", got, attemptsBeforeGivingUp-1)
		}
		if _, err := client.GetMetadata(context.Background()); err != nil {
			t.Errorf("the next GetMetadata error = %v, want the complete answer", err)
		}
		if got := len(service.received()); got != attemptsBeforeGivingUp+1 {
			t.Errorf("the agent asked %d times in all, want %d: nothing was held after giving up", got, attemptsBeforeGivingUp+1)
		}
	})

	t.Run("after giving up, the next fetch makes 10 attempts of its own", func(t *testing.T) {
		service, address := newTokenService(t, unavailable)
		client, _ := newAgentClient(address)
		_, _ = client.GetMetadataWithRetry(context.Background())

		_, err := client.GetMetadataWithRetry(context.Background())

		if !errors.Is(err, retry.ErrMaxRetriesExceeded) {
			t.Errorf("GetMetadataWithRetry error = %v, want %v", err, retry.ErrMaxRetriesExceeded)
		}
		if got := len(service.received()); got != 2*attemptsBeforeGivingUp {
			t.Errorf("the agent asked the metadata service %d times, want %d", got, 2*attemptsBeforeGivingUp)
		}
	})

	t.Run("with no answer at all the agent waits 10s between attempts, and gives up after the 10th", func(t *testing.T) {
		client, clock := newAgentClient(unreachableAddress(t))

		_, err := client.GetMetadataWithRetry(context.Background())

		if !errors.Is(err, retry.ErrMaxRetriesExceeded) {
			t.Errorf("GetMetadataWithRetry error = %v, want %v", err, retry.ErrMaxRetriesExceeded)
		}
		waits := clock.waited()
		if len(waits) != attemptsBeforeGivingUp-1 {
			t.Fatalf("the agent waited %v, want %d waits", waits, attemptsBeforeGivingUp-1)
		}
		for _, wait := range waits {
			if wait != retryDelayWithoutRetryAfter {
				t.Errorf("the agent waited %v, want %v each time", waits, retryDelayWithoutRetryAfter)
				break
			}
		}
	})

	for _, row := range []struct {
		name       string
		retryAfter string
		wantWait   time.Duration
	}{
		{"no Retry-After", "", 10 * time.Second},
		{"Retry-After 5", "5", 5 * time.Second},
		{"Retry-After 45", "45", 45 * time.Second},
		{"Retry-After 3600", "3600", 60 * time.Second},
		{"a Retry-After date 20s ahead", startOfTest.Add(20 * time.Second).Format(http.TimeFormat), 20 * time.Second},
		{"a Retry-After date in the past", startOfTest.Add(-time.Hour).Format(http.TimeFormat), 0},
		{"Retry-After abc", "abc", 10 * time.Second},
	} {
		t.Run("after a failed answer with "+row.name+" the agent waits "+row.wantWait.String()+" before asking again", func(t *testing.T) {
			failed := unavailable
			failed.retryAfter = row.retryAfter
			_, address := newTokenService(t, failed, ok(complete))
			client, clock := newAgentClient(address)

			_, err := client.GetMetadataWithRetry(context.Background())

			if err != nil {
				t.Fatalf("GetMetadataWithRetry error = %v, want the second answer", err)
			}
			if waits := clock.waited(); len(waits) != 1 || waits[0] != row.wantWait {
				t.Errorf("the agent waited %v, want [%v]", waits, row.wantWait)
			}
		})
	}

	t.Run("a 200 missing a value with Retry-After 5 makes the agent wait 5s before asking again", func(t *testing.T) {
		incomplete := ok(answerWithout("api-url"))
		incomplete.retryAfter = "5"
		_, address := newTokenService(t, incomplete, ok(complete))
		client, clock := newAgentClient(address)

		_, err := client.GetMetadataWithRetry(context.Background())

		if err != nil {
			t.Fatalf("GetMetadataWithRetry error = %v, want the second answer", err)
		}
		if waits, want := clock.waited(), 5*time.Second; len(waits) != 1 || waits[0] != want {
			t.Errorf("the agent waited %v, want [%v]", waits, want)
		}
	})

	// A knock has 2 minutes; a Retry-After of 60 s can outlast what is left.
	t.Run("a fetch stops waiting to ask again when its caller's time runs out", func(t *testing.T) {
		failed := unavailable
		failed.retryAfter = "60"
		service, address := newTokenService(t, failed)
		client := NewClient(address, time.Second, zap.NewNop())
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		_, err := client.GetMetadataWithRetry(ctx)

		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("GetMetadataWithRetry error = %v, want the caller's deadline", err)
		}
		if got := len(service.received()); got != 1 {
			t.Errorf("the agent asked the metadata service %d times, want 1", got)
		}
	})

	t.Run("the agent never falls back to the old path when /v1/token fails", func(t *testing.T) {
		service, address := newTokenService(t, tokenServiceAnswer{status: http.StatusNotFound, body: "not found"})
		client, _ := newAgentClient(address)

		_, err := client.GetMetadataWithRetry(context.Background())

		if err == nil {
			t.Error("GetMetadataWithRetry error = nil, want the attempts to fail")
		}
		for _, request := range service.received() {
			if request.URL.Path != "/v1/token" {
				t.Errorf("the agent asked %s, want only /v1/token (never %s)", request.URL.Path, oldPath)
			}
		}
	})

	t.Run("two fetches at the same moment make one request and both get its values", func(t *testing.T) {
		service, address := newTokenService(t, ok(complete))
		client, _ := newAgentClient(address)
		var wait sync.WaitGroup
		tokens := make([]string, 2)

		for index := range tokens {
			wait.Add(1)
			go func() {
				defer wait.Done()
				if info, err := client.GetMetadataWithRetry(context.Background()); err == nil {
					tokens[index] = info.Token
				}
			}()
		}
		wait.Wait()

		if tokens[0] != issuedToken || tokens[1] != issuedToken {
			t.Errorf("the two fetches got %q, want %q twice", tokens, issuedToken)
		}
		if got := len(service.received()); got != 1 {
			t.Errorf("the agent asked the metadata service %d times, want 1", got)
		}
	})
}
