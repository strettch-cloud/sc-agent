package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	retry "github.com/strettch-cloud/sc-agent/pkg/connectionretry"
	"go.uber.org/zap"
)

const (
	// HTTP headers
	HeaderAccept      = "Accept"
	HeaderXResourceID = "X-Resource-ID"
	AcceptJSON        = "application/json"

	// Default settings
	defaultCacheDuration       = 30 * time.Minute
	defaultMaxIdleConns        = 10
	defaultMaxIdleConnsPerHost = 10
	defaultIdleConnTimeout     = 90 * time.Second
)

// Info contains metadata service response data
type Info struct {
	Token       string
	PublicKey   string
	CloudAPIURL string
}

// Response represents the raw API response
type Response struct {
	Token       string  `json:"token"`
	PublicKey   *string `json:"publicKey,omitempty"`
	CloudAPIUrl string  `json:"cloudAPIUrl"`
}

// Client handles metadata service communication with caching
type Client struct {
	endpoint      string
	httpClient    *http.Client
	logger        *zap.Logger
	cacheDuration time.Duration

	// Cache fields
	mu        sync.RWMutex
	cache     *Info
	cacheTime time.Time
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
		logger:        logger,
		cacheDuration: defaultCacheDuration,
	}
}

// GetMetadata retrieves metadata (token, public key, and API URL)
func (c *Client) GetMetadata(ctx context.Context, resourceID string) (*Info, error) {
	// Check cache first
	if info := c.getCached(); info != nil {
		return info, nil
	}

	// Fetch fresh data
	return c.fetchMetadata(ctx, resourceID)
}

// GetMetadataWithRetry retrieves metadata with retry logic
func (c *Client) GetMetadataWithRetry(ctx context.Context, resourceID string, maxAttempts int) (*Info, error) {
	cfg := retry.Default().WithMaxAttempts(maxAttempts)

	return retry.Execute(ctx, c.logger, "get-metadata",
		func(ctx context.Context) (*Info, error) {
			return c.GetMetadata(ctx, resourceID)
		}, cfg)
}

// GetMetadataForStartup retrieves metadata with infinite retry for startup scenarios
func (c *Client) GetMetadataForStartup(ctx context.Context, resourceID string) (*Info, error) {
	// Use default config (infinite retries)
	return retry.Execute(ctx, c.logger, "get-metadata-startup",
		func(ctx context.Context) (*Info, error) {
			return c.GetMetadata(ctx, resourceID)
		}, nil)
}

// fetchMetadata retrieves fresh metadata from the service
func (c *Client) fetchMetadata(ctx context.Context, resourceID string) (*Info, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check cache after acquiring lock
	if c.cache != nil && time.Since(c.cacheTime) < c.cacheDuration {
		return c.cache, nil
	}

	// Make request
	url := fmt.Sprintf("%s?includePublicKey=True", c.endpoint)
	resp, err := c.doRequest(ctx, url, resourceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		c.logger.Error("Metadata service error",
			zap.Int("status", resp.StatusCode),
			zap.String("body", string(body)))
		return nil, fmt.Errorf("metadata service returned %d: %s", resp.StatusCode, string(body))
	}

	// Parse response
	var apiResp Response
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if apiResp.Token == "" {
		return nil, fmt.Errorf("empty token in response")
	}

	// Build info object
	info := &Info{
		Token:       apiResp.Token,
		CloudAPIURL: apiResp.CloudAPIUrl,
	}
	if apiResp.PublicKey != nil {
		info.PublicKey = *apiResp.PublicKey
	}

	// Update cache
	c.cache = info
	c.cacheTime = time.Now()

	c.logger.Debug("Metadata cached",
		zap.Time("cached_at", c.cacheTime),
		zap.Duration("ttl", c.cacheDuration))

	return info, nil
}

// doRequest performs an HTTP GET request
func (c *Client) doRequest(ctx context.Context, url, resourceID string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set(HeaderAccept, AcceptJSON)
	req.Header.Set(HeaderXResourceID, resourceID)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.logger.Error("Request failed",
			zap.String("url", url),
			zap.Error(err))
		return nil, fmt.Errorf("request failed: %w", err)
	}

	return resp, nil
}

// getCached returns cached info if valid
func (c *Client) getCached() *Info {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.cache != nil && time.Since(c.cacheTime) < c.cacheDuration {
		ttl := c.cacheDuration - time.Since(c.cacheTime)
		c.logger.Debug("Using cached metadata", zap.Duration("ttl", ttl))
		return c.cache
	}

	return nil
}

// Close releases resources
func (c *Client) Close() {
	c.httpClient.CloseIdleConnections()
}
