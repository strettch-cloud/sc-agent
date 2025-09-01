package console

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.uber.org/zap"
	"github.com/strettch/strettch-cloud/sc-agent/pkg/clients/cloudapi"
	"github.com/strettch/strettch-cloud/sc-agent/pkg/clients/metadata"
	"github.com/strettch/strettch-cloud/sc-agent/pkg/config"
	"github.com/strettch/strettch-cloud/sc-agent/pkg/ssh"
)

// HTTPHandler handles legacy HTTP-based console notifications
type HTTPHandler struct {
	config         *config.Config
	logger         *zap.Logger
	metadataClient *metadata.Client
	backendClient  *backend.Client
	keyManager     *ssh.KeyManager
}

// NotifyPayload represents the HTTP notification payload
type NotifyPayload struct {
	SessionID string `json:"session_id"`
}

// NewHTTPHandler creates a new HTTP notification handler
func NewHTTPHandler(cfg *config.Config, logger *zap.Logger) (*HTTPHandler, error) {
	metadataClient := metadata.NewClient(cfg.MetadataServiceEndpoint, cfg.HTTPTimeout, logger)
	backendClient := backend.NewClient(cfg.BackendEndpoint, cfg.ResourceID, cfg.HTTPTimeout, logger)
	
	keyManager, err := ssh.NewKeyManager(logger)
	if err != nil {
		return nil, err
	}

	return &HTTPHandler{
		config:         cfg,
		logger:         logger,
		metadataClient: metadataClient,
		backendClient:  backendClient,
		keyManager:     keyManager,
	}, nil
}

// ServeHTTP handles HTTP console notification requests
func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.logger.Warn("Invalid HTTP method for notify endpoint",
			zap.String("method", r.Method),
			zap.String("remote_addr", r.RemoteAddr))
		http.Error(w, "Only POST allowed", http.StatusMethodNotAllowed)
		return
	}

	h.logger.Info("Received HTTP console notification",
		zap.String("remote_addr", r.RemoteAddr),
		zap.String("user_agent", r.UserAgent()))

	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.logger.Error("Failed to read request body", zap.Error(err))
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}

	var payload NotifyPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		h.logger.Error("Failed to parse JSON payload", zap.Error(err))
		http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}

	if payload.SessionID == "" {
		h.logger.Error("Empty session ID in payload")
		http.Error(w, "Session ID is required", http.StatusBadRequest)
		return
	}

	h.logger.Info("Processing HTTP console notification",
		zap.String("session_id", payload.SessionID))

	// Process the notification
	if err := h.processNotification(r.Context()); err != nil {
		h.logger.Error("Failed to process console notification",
			zap.String("session_id", payload.SessionID),
			zap.Error(err))
		http.Error(w, "Failed to process notification", http.StatusInternalServerError)
		return
	}

	h.logger.Info("HTTP console notification processed successfully",
		zap.String("session_id", payload.SessionID))

	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte("Key installed")); err != nil {
		h.logger.Warn("Failed to write response", zap.Error(err))
	}
}

// processNotification handles the console notification processing
func (h *HTTPHandler) processNotification(ctx context.Context) error {
	// Create timeout context
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Get authentication token from metadata service
	authToken, err := h.metadataClient.GetAuthTokenWithRetry(ctx, h.config.ResourceID, h.config.MaxRetries, h.config.RetryInterval)
	if err != nil {
		return fmt.Errorf("failed to get authentication token: %w", err)
	}

	// Fetch SAT key from strettch-cloud-api
	satKey, err := h.backendClient.GetSATKeyWithRetry(ctx, authToken, h.config.MaxRetries, h.config.RetryInterval)
	if err != nil {
		return fmt.Errorf("failed to fetch SAT key: %w", err)
	}

	// Install SAT key
	h.logger.Debug("Installing SAT key")
	if err := h.keyManager.InstallSATKey(satKey); err != nil {
		return fmt.Errorf("failed to install SAT key: %w", err)
	}

	return nil
}

// Close cleans up resources
func (h *HTTPHandler) Close() error {
	if h.metadataClient != nil {
		h.metadataClient.Close()
	}
	if h.backendClient != nil {
		h.backendClient.Close()
	}
	return nil
}