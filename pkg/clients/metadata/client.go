package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	retry "github.com/strettch-cloud/sc-agent/pkg/connectionretry"
	"go.uber.org/zap"
)

const (
	// HTTP headers
	HeaderAccept = "Accept"
	AcceptJSON   = "application/json"

	// How long the metadata service asks the agent to wait before asking again.
	HeaderRetryAfter = "Retry-After"

	// The metadata service at 169.254.169.254 refuses a request without this header.
	HeaderMetadataFlavor = "Metadata-Flavor"
	MetadataFlavor       = "StrettchCloud"

	// How long values are held at most, and how long before its expiry a token is no longer used.
	cacheDuration = 30 * time.Minute
	expiryMargin  = time.Minute

	// Every token fetch asks at most maxAttempts times, retryDelay apart unless the service's Retry-After asks
	// for another wait, which is capped at maxRetryAfter.
	maxAttempts   = 10
	maxRetryAfter = time.Minute

	defaultMaxIdleConns        = 10
	defaultMaxIdleConnsPerHost = 10
	defaultIdleConnTimeout     = 90 * time.Second
)

// Info contains metadata service response data
type Info struct {
	Token       string
	PublicKey   string
	CloudAPIURL string
	ExpiresAt   time.Time
}

// Response is the metadata service's answer on /v1/token for an agent in a VM.
type Response struct {
	Token       string `json:"token"`
	ExpiresAt   string `json:"expires-at"`
	PublicKey   string `json:"console-public-key"`
	CloudAPIUrl string `json:"api-url"`
}

// retryDelay is a variable only so the end-to-end build can shorten it (overrides_e2e.go).
var retryDelay = 10 * time.Second

// Client handles metadata service communication with caching
type Client struct {
	endpoint   string
	httpClient *http.Client
	logger     *zap.Logger

	// now and wait are replaced by tests so they run without real clocks or delays.
	now  func() time.Time
	wait func(ctx context.Context, d time.Duration) error

	mu        sync.RWMutex
	cache     *Info
	heldUntil time.Time
}

// NewClient creates a new metadata service client
func NewClient(endpoint string, timeout time.Duration, logger *zap.Logger) *Client {
	transport := &http.Transport{
		MaxIdleConns:        defaultMaxIdleConns,
		MaxIdleConnsPerHost: defaultMaxIdleConnsPerHost,
		IdleConnTimeout:     defaultIdleConnTimeout,
	}

	return &Client{
		endpoint: endpoint,
		httpClient: &http.Client{
			Timeout:   timeout,
			Transport: transport,
		},
		logger: logger,
		now:    time.Now,
		wait:   sleep,
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// GetMetadata returns the held values, or asks the metadata service once.
func (c *Client) GetMetadata(ctx context.Context) (*Info, error) {
	if info := c.getCached(); info != nil {
		return info, nil
	}
	info, _, err := c.fetchMetadata(ctx)
	return info, err
}

// GetMetadataWithRetry asks up to maxAttempts times, waiting retryDelay between attempts, or what the service's
// Retry-After asks for, up to maxRetryAfter.
func (c *Client) GetMetadataWithRetry(ctx context.Context) (*Info, error) {
	if info := c.getCached(); info != nil {
		return info, nil
	}
	for attempt := 1; ; attempt++ {
		info, retryAfter, err := c.fetchMetadata(ctx)
		if err == nil {
			return info, nil
		}
		if attempt == maxAttempts {
			c.logger.Error("Metadata service failed on every attempt",
				zap.Int("attempts", attempt), zap.Error(err))
			return nil, fmt.Errorf("%w: %w", retry.ErrMaxRetriesExceeded, err)
		}
		delay := retryDelay
		if retryAfter >= 0 {
			delay = min(retryAfter, maxRetryAfter)
		}
		c.logger.Warn("Asking the metadata service again",
			zap.Int("attempt", attempt), zap.Duration("retry_in", delay), zap.Error(err))
		if err := c.wait(ctx, delay); err != nil {
			return nil, err
		}
	}
}

// fetchMetadata asks the metadata service once. retryAfter is the wait the answer asked for, or -1 when it
// asked for none the agent can read.
func (c *Client) fetchMetadata(ctx context.Context) (info *Info, retryAfter time.Duration, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Another caller may have fetched while this one waited for the lock.
	if c.cache != nil && c.now().Before(c.heldUntil) {
		return c.cache, -1, nil
	}

	resp, err := c.doRequest(ctx, c.endpoint)
	if err != nil {
		return nil, -1, err
	}
	defer func() { _ = resp.Body.Close() }()
	retryAfter = parseRetryAfter(resp.Header.Get(HeaderRetryAfter), c.now())

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, retryAfter, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		c.logger.Error("Metadata service error",
			zap.Int("status", resp.StatusCode),
			zap.String("body", string(body)))
		return nil, retryAfter, fmt.Errorf("metadata service returned %d: %s", resp.StatusCode, string(body))
	}

	info, err = c.parseAnswer(body)
	if err != nil {
		return nil, retryAfter, err
	}

	// Held for at most cacheDuration, and never past expiryMargin before the token expires.
	c.cache = info
	c.heldUntil = minTime(c.now().Add(cacheDuration), info.ExpiresAt.Add(-expiryMargin))
	c.logger.Debug("Metadata held", zap.Time("held_until", c.heldUntil))
	return info, -1, nil
}

// parseAnswer refuses an answer missing any of the four values, or whose token is about to expire, so the next
// attempt asks again.
func (c *Client) parseAnswer(body []byte) (*Info, error) {
	var apiResp Response
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	if apiResp.Token == "" {
		return nil, fmt.Errorf("empty token in response")
	}
	if apiResp.CloudAPIUrl == "" {
		return nil, fmt.Errorf("empty API address in response")
	}
	if apiResp.PublicKey == "" {
		return nil, fmt.Errorf("empty console public key in response")
	}
	expiresAt, err := time.Parse(time.RFC3339, apiResp.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("unreadable token expiry %q in response: %w", apiResp.ExpiresAt, err)
	}
	if !c.now().Add(expiryMargin).Before(expiresAt) {
		return nil, fmt.Errorf("token expires at %s, too soon to use", apiResp.ExpiresAt)
	}
	return &Info{
		Token:       apiResp.Token,
		PublicKey:   apiResp.PublicKey,
		CloudAPIURL: apiResp.CloudAPIUrl,
		ExpiresAt:   expiresAt,
	}, nil
}

// parseRetryAfter reads a Retry-After of seconds or an HTTP date, and returns -1 when there is none it can read.
// A date in the past means no wait.
func parseRetryAfter(value string, now time.Time) time.Duration {
	if value == "" {
		return -1
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds < 0 {
			return -1
		}
		return time.Duration(seconds) * time.Second
	}
	date, err := http.ParseTime(value)
	if err != nil {
		return -1
	}
	return max(date.Sub(now), 0)
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// doRequest performs an HTTP GET request
func (c *Client) doRequest(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set(HeaderAccept, AcceptJSON)
	req.Header.Set(HeaderMetadataFlavor, MetadataFlavor)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.logger.Error("Request failed",
			zap.String("url", url),
			zap.Error(err))
		return nil, fmt.Errorf("request failed: %w", err)
	}

	return resp, nil
}

// getCached returns the held values while they may still be used.
func (c *Client) getCached() *Info {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.cache != nil && c.now().Before(c.heldUntil) {
		return c.cache
	}
	return nil
}

// Close releases resources
func (c *Client) Close() {
	c.httpClient.CloseIdleConnections()
}
