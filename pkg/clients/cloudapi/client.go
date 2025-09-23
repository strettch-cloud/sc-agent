package cloudapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/strettch/strettch-cloud/sc-agent/pkg/clients/metadata"
	retry "github.com/strettch/strettch-cloud/sc-agent/pkg/connectionretry"
	"go.uber.org/zap"
)

const (
	// HTTP headers
	HeaderAccept        = "Accept"
	HeaderAuthorization = "Authorization"
	HeaderUser          = "user"
	AcceptJSON          = "application/json"

	// Default transport settings
	defaultMaxIdleConns        = 10
	defaultMaxIdleConnsPerHost = 10
	defaultIdleConnTimeout     = 90 * time.Second
)

// ConsoleKey represents the console access key data
type ConsoleKey struct {
	PublicKey   string `json:"publicKey"`
	Principal   string `json:"principal"`   // Usually "root" for console access
	RequesterID string `json:"requesterId"` // Email of the user who requested access
	CreatedAt   string `json:"createdAt"`   // When the key was created (RFC3339)
	ExpireAt    string `json:"expireAt"`    // When the key expires (RFC3339)
}

// ConsoleKeyResponse wraps the API response
type ConsoleKeyResponse struct {
	Data ConsoleKey `json:"data"`
}

// Client handles cloud API interactions
type Client struct {
	httpClient     *http.Client
	logger         *zap.Logger
	resourceID     string
	metadataClient *metadata.Client
}

// NewClient creates a new cloud API client
func NewClient(metadataClient *metadata.Client, resourceID string, timeout time.Duration, logger *zap.Logger) *Client {
	transport := &http.Transport{
		MaxIdleConns:        defaultMaxIdleConns,
		MaxIdleConnsPerHost: defaultMaxIdleConnsPerHost,
		IdleConnTimeout:     defaultIdleConnTimeout,
	}

	return &Client{
		resourceID:     resourceID,
		metadataClient: metadataClient,
		httpClient: &http.Client{
			Timeout:   timeout,
			Transport: transport,
		},
		logger: logger,
	}
}

// FetchConsoleKey retrieves the console key data
func (c *Client) FetchConsoleKey(ctx context.Context, authToken string) (*ConsoleKey, error) {
	cloudAPI, err := c.getCloudAPIEndpoint(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get cloud API endpoint: %w", err)
	}

	url := fmt.Sprintf("%s/resource-manager/api/v1/console/public-key/agent", cloudAPI)

	resp, err := c.doRequest(ctx, url, authToken)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			// Log error but don't fail the function
			_ = err
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		c.logger.Error("API error",
			zap.Int("status", resp.StatusCode),
			zap.String("body", string(body)))
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	var response ConsoleKeyResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if response.Data.PublicKey == "" {
		return nil, fmt.Errorf("empty public key in response")
	}

	c.logger.Debug("Console key fetched successfully")
	return &response.Data, nil
}

// FetchConsoleKeyWithRetry retrieves console key with retry logic
func (c *Client) FetchConsoleKeyWithRetry(ctx context.Context, authToken string, maxAttempts int) (*ConsoleKey, error) {
	cfg := retry.Default().WithMaxAttempts(maxAttempts)

	return retry.Execute(ctx, c.logger, "fetch-console-key",
		func(ctx context.Context) (*ConsoleKey, error) {
			return c.FetchConsoleKey(ctx, authToken)
		}, cfg)
}

// doRequest performs an HTTP GET request with auth
func (c *Client) doRequest(ctx context.Context, url, authToken string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set(HeaderAuthorization, "Bearer "+authToken)
	req.Header.Set(HeaderAccept, AcceptJSON)

	c.logger.Debug("Executing request", zap.String("url", url))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	return resp, nil
}

// getCloudAPIEndpoint retrieves the cloud API URL with retry
func (c *Client) getCloudAPIEndpoint(ctx context.Context) (string, error) {
	// Try cached value first
	info, err := c.metadataClient.GetMetadata(ctx, c.resourceID)
	if err == nil {
		return info.CloudAPIURL, nil
	}

	c.logger.Debug("Cache miss, fetching with retry", zap.Error(err))

	// Use limited retries for runtime operations
	cfg := retry.Default().WithMaxAttempts(5)

	info, err = retry.Execute(ctx, c.logger, "get-cloud-api-endpoint",
		func(ctx context.Context) (*metadata.Info, error) {
			return c.metadataClient.GetMetadata(ctx, c.resourceID)
		}, cfg)

	if err != nil {
		return "", fmt.Errorf("failed to get metadata: %w", err)
	}

	return info.CloudAPIURL, nil
}

// Close releases resources
func (c *Client) Close() {
	c.httpClient.CloseIdleConnections()
}
