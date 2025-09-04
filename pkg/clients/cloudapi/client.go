package cloudapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/strettch/strettch-cloud/sc-agent/pkg/clients/metadata"
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

// SATKeyResponse 
type SATKeyResponse struct {
	Data struct {
		SATKey string `json:"satKey"`
	} `json:"data"`
}

// Client 
type Client struct {
	httpClient     *http.Client
	logger         *zap.Logger
	resourceID     string
	metadataClient *metadata.Client
}

// NewClient creates a new cloud API client
func NewClient(metadataClient *metadata.Client, resourceID string, timeout time.Duration, logger *zap.Logger) *Client {
	return &Client{
		resourceID:     resourceID,
		metadataClient: metadataClient,
		httpClient: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				MaxIdleConns:        defaultMaxIdleConns,
				MaxIdleConnsPerHost: defaultMaxIdleConnsPerHost,
				IdleConnTimeout:     defaultIdleConnTimeout,
			},
		},
		logger: logger,
	}
}

// GetSATKey fetches the SAT public key 
func (c *Client) GetSATKey(ctx context.Context, authToken string) (string, error) {
	_, _, cloudAPIUrl, err := c.metadataClient.GetAuthToken(ctx, c.resourceID)
	if err != nil {
		return "", fmt.Errorf("failed to get cloud API URL: %w", err)
	}
	
	url := fmt.Sprintf("%s/resource-manager/api/v1/console/public-key/agent", cloudAPIUrl)

	req, err := c.createRequest(ctx, url, authToken)
	if err != nil {
		return "", err
	}

	c.logger.Debug("Fetching SAT key", zap.String("url", url))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch SAT key: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			c.logger.Warn("Failed to close response body", zap.Error(closeErr))
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		c.logger.Error("Returned error",
			zap.Int("status_code", resp.StatusCode),
			zap.String("response", string(body)))
		return "", fmt.Errorf("GetSATKey returned status %d: %s", resp.StatusCode, string(body))
	}

	var keyResp SATKeyResponse
	if err := json.Unmarshal(body, &keyResp); err != nil {
		return "", fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if keyResp.Data.SATKey == "" {
		return "", fmt.Errorf("empty SAT key in response")
	}

	c.logger.Debug("Successfully fetched SAT key")
	return keyResp.Data.SATKey, nil
}

// GetSATKeyWithRetry fetches a SAT key with retry logic
func (c *Client) GetSATKeyWithRetry(ctx context.Context, authToken string, maxRetries int, retryDelay time.Duration) (string, error) {
	var lastErr error
	
	for attempt := 0; attempt <= maxRetries; attempt++ {
		// Check context before each attempt
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		
		// Wait before retry (skip on first attempt)
		if attempt > 0 {
			c.logger.Debug("Retrying SAT key request",
				zap.Int("attempt", attempt),
				zap.Duration("delay", retryDelay))
			
			select {
			case <-time.After(retryDelay):
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		
		satKey, err := c.GetSATKey(ctx, authToken)
		if err == nil {
			return satKey, nil
		}
		
		lastErr = err
		c.logger.Warn("SAT key request failed", 
			zap.Error(err), 
			zap.Int("attempt", attempt+1),
			zap.Int("max_attempts", maxRetries+1))
	}
	
	return "", fmt.Errorf("exhausted %d attempts: %w", maxRetries+1, lastErr)
}

// createRequest builds the HTTP request
func (c *Client) createRequest(ctx context.Context, url, authToken string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set(HeaderAuthorization, "Bearer "+authToken)
	req.Header.Set(HeaderAccept, AcceptJSON)

	return req, nil
}

// Close closes the HTTP client's idle connections
func (c *Client) Close() {
	c.httpClient.CloseIdleConnections()
}