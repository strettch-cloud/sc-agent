package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"
)

const (
	// HTTP headers
	HeaderAccept      = "Accept"
	HeaderXResourceID = "X-Resource-ID"
	AcceptJSON        = "application/json"
	
	// Default settings
	defaultTokenCacheLifetime  = 30 * time.Minute
	defaultMaxIdleConns        = 10
	defaultMaxIdleConnsPerHost = 10
	defaultIdleConnTimeout     = 90 * time.Second
)

// TokenResponse represents the response from the metadata service
type TokenResponse struct {
	Token     string  `json:"token"`
	PublicKey *string `json:"publicKey,omitempty"`
}

// Client handles communication with the metadata service with token caching
type Client struct {
	endpoint      string
	httpClient    *http.Client
	logger        *zap.Logger
	
	// Token cache
	mu            sync.RWMutex
	cachedToken   string
	tokenExpiry   time.Time
	tokenLifetime time.Duration
}

// NewClient creates a new metadata service client with token caching
func NewClient(endpoint string, timeout time.Duration, logger *zap.Logger) *Client {
	return &Client{
		endpoint: endpoint,
		httpClient: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				MaxIdleConns:        defaultMaxIdleConns,
				MaxIdleConnsPerHost: defaultMaxIdleConnsPerHost,
				IdleConnTimeout:     defaultIdleConnTimeout,
			},
		},
		logger:        logger,
		tokenLifetime: defaultTokenCacheLifetime,
	}
}


// GetAuthTokenWithRetry fetches an auth token with retry logic
func (c *Client) GetAuthTokenWithRetry(ctx context.Context, resourceID string, maxRetries int, retryDelay time.Duration) (string, error) {
	var lastErr error
	
	for attempt := 0; attempt <= maxRetries; attempt++ {
		// Check context before each attempt
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		
		// Wait before retry (skip on first attempt)
		if attempt > 0 {
			c.logger.Debug("Retrying auth token request",
				zap.Int("attempt", attempt),
				zap.Duration("delay", retryDelay))
			
			select {
			case <-time.After(retryDelay):
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		
		token, _, err := c.GetAuthToken(ctx, resourceID)
		if err == nil {
			return token, nil
		}
		
		lastErr = err
		c.logger.Warn("Auth token request failed", 
			zap.Error(err), 
			zap.Int("attempt", attempt+1),
			zap.Int("max_attempts", maxRetries+1))
	}
	
	return "", fmt.Errorf("exhausted %d attempts: %w", maxRetries+1, lastErr)
}


// GetAuthTokenWithPublicKey fetches auth token and public key from metadata service
func (c *Client) GetAuthToken(ctx context.Context, resourceID string) (token string, publicKey string, err error) {
	// Try to use cached token
	if cachedToken := c.getCachedToken(); cachedToken != "" {
		return cachedToken, "", nil
	}

	// Acquire write lock to fetch new token
	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check after acquiring write lock
	if c.cachedToken != "" && time.Now().Before(c.tokenExpiry) {
		return c.cachedToken, "", nil
	}

	// Build URL 
	url := fmt.Sprintf("%s?includePublicKey=True", c.endpoint)
	
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set(HeaderAccept, AcceptJSON)
	req.Header.Set(HeaderXResourceID, resourceID)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.logger.Error("Failed to fetch auth token with public key", 
			zap.String("endpoint", url),
			zap.Error(err))
		return "", "", fmt.Errorf("failed to fetch auth token: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			c.logger.Warn("Failed to close response body", zap.Error(closeErr))
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		c.logger.Error("Metadata service error",
			zap.Int("status", resp.StatusCode),
			zap.String("body", string(body)))
		return "", "", fmt.Errorf("metadata service returned %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp TokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", "", fmt.Errorf("failed to parse response: %w", err)
	}

	if tokenResp.Token == "" {
		return "", "", fmt.Errorf("empty token in response")
	}
	
	// Update cache
	c.cachedToken = tokenResp.Token
	c.tokenExpiry = time.Now().Add(c.tokenLifetime)
	
	c.logger.Debug("Cached new auth token",
		zap.Time("expires_at", c.tokenExpiry),
		zap.Duration("lifetime", c.tokenLifetime))
	
	publicKeyValue := ""
	if tokenResp.PublicKey != nil {
		publicKeyValue = *tokenResp.PublicKey
	}

	return tokenResp.Token, publicKeyValue, nil
}

// getCachedToken returns the cached token if it's still valid
func (c *Client) getCachedToken() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	
	if c.cachedToken != "" && time.Now().Before(c.tokenExpiry) {
		c.logger.Debug("Using cached auth token", 
			zap.Duration("ttl", time.Until(c.tokenExpiry)))
		return c.cachedToken
	}
	
	return ""
}


// Close closes the HTTP client's idle connections
func (c *Client) Close() {
	c.httpClient.CloseIdleConnections()
}