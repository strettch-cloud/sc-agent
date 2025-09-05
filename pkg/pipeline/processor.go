package pipeline

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/strettch/strettch-cloud/sc-console-agent/pkg/clients/cloudapi"
	"github.com/strettch/strettch-cloud/sc-console-agent/pkg/clients/metadata"
	"github.com/strettch/strettch-cloud/sc-console-agent/pkg/config"
	"github.com/strettch/strettch-cloud/sc-console-agent/pkg/console"
	"github.com/strettch/strettch-cloud/sc-console-agent/pkg/ssh"
	"go.uber.org/zap"
)

const (
	// Default timeouts
	knockProcessTimeout = 30 * time.Second
)

// Processor manages the console access pipeline
type Processor struct {
	config         *config.Config
	logger         *zap.Logger
	metadataClient *metadata.Client
	cloudAPIClient *cloudapi.Client
	keyManager     *ssh.KeyManager
	udpListener    *console.UDPListener

	cleanupTicker *time.Ticker
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
}

// NewProcessor creates a new pipeline processor
func NewProcessor(cfg *config.Config, logger *zap.Logger) (*Processor, error) {
	keyManager, err := ssh.NewKeyManager(logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create key manager: %w", err)
	}

	udpListener, err := console.NewUDPListener(cfg, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create UDP listener: %w", err)
	}

	metadataClient := metadata.NewClient(cfg.MetadataServiceEndpoint, cfg.HTTPTimeout, logger)
	cloudAPIClient := cloudapi.NewClient(metadataClient, cfg.ResourceID, cfg.HTTPTimeout, logger)

	ctx, cancel := context.WithCancel(context.Background())

	return &Processor{
		config:         cfg,
		logger:         logger,
		metadataClient: metadataClient,
		cloudAPIClient: cloudAPIClient,
		keyManager:     keyManager,
		udpListener:    udpListener,
		cleanupTicker:  time.NewTicker(cfg.KeyCleanupInterval),
		ctx:            ctx,
		cancel:         cancel,
	}, nil
}

// Start initializes and starts the processor
func (p *Processor) Start() error {
	p.logger.Info("Starting console access processor")

	// Initialize with signing key from metadata service
	if err := p.initialize(); err != nil {
		return fmt.Errorf("initialization failed: %w", err)
	}

	// Start UDP listener
	if err := p.udpListener.Start(); err != nil {
		return fmt.Errorf("failed to start UDP listener: %w", err)
	}

	// Start background tasks
	p.startWorkers()

	p.logger.Info("Console access processor started successfully")
	return nil
}

// Stop gracefully shuts down the processor
func (p *Processor) Stop() error {
	p.logger.Info("Stopping console access processor")

	// Signal shutdown
	p.cancel()

	// Stop UDP listener
	if err := p.udpListener.Stop(); err != nil {
		p.logger.Error("Error stopping UDP listener", zap.Error(err))
	}

	// Stop cleanup ticker
	if p.cleanupTicker != nil {
		p.cleanupTicker.Stop()
	}

	// Wait for workers to complete
	p.wg.Wait()

	// Close clients
	p.closeClients()

	p.logger.Info("Console access processor stopped")
	return nil
}

// initialize fetches initial metadata and configures the UDP listener
func (p *Processor) initialize() error {
	p.logger.Info("Initializing with metadata service")
	
	ctx, cancel := context.WithCancel(p.ctx)
	defer cancel()
	
	// Get metadata with infinite retry for startup
	metadata, err := p.metadataClient.GetMetadataForStartup(ctx, p.config.ResourceID)
	if err != nil {
		return fmt.Errorf("failed to fetch metadata: %w", err)
	}

	if metadata.PublicKey == "" {
		return fmt.Errorf("metadata service did not provide public key")
	}

	// Configure UDP listener with public key
	if err := p.udpListener.UpdatePublicKey(metadata.PublicKey); err != nil {
		return fmt.Errorf("failed to set public key: %w", err)
	}

	p.logger.Info("Initialized UDP listener with public key")
	return nil
}

// startWorkers launches background goroutines
func (p *Processor) startWorkers() {
	// Event processing worker
	p.wg.Add(1)
	go p.processEvents()

	// Key cleanup worker
	p.wg.Add(1)
	go p.cleanupKeys()
}

// processEvents handles UDP knock events
func (p *Processor) processEvents() {
	defer p.wg.Done()

	eventChan := p.udpListener.GetEventChannel()
	
	for {
		select {
		case <-p.ctx.Done():
			return
		case event, ok := <-eventChan:
			if !ok {
				p.logger.Debug("Event channel closed")
				return
			}
			p.handleKnock(event)
		}
	}
}

// cleanupKeys periodically removes expired console keys
func (p *Processor) cleanupKeys() {
	defer p.wg.Done()

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-p.cleanupTicker.C:
			if err := p.keyManager.RemoveConsoleKeys(); err != nil {
				p.logger.Error("Key cleanup failed", zap.Error(err))
			} else {
				p.logger.Debug("Key cleanup completed")
			}
		}
	}
}

// handleKnock processes a single knock event
func (p *Processor) handleKnock(event console.UDPKnockEvent) {
	logger := p.logger.With(
		zap.String("source_ip", event.SourceIP),
		zap.Time("knock_time", time.Unix(event.Timestamp, 0)))
	
	logger.Info("Processing UDP knock")

	ctx, cancel := context.WithTimeout(p.ctx, knockProcessTimeout)
	defer cancel()

	// Process the knock
	if err := p.processKnock(ctx, logger); err != nil {
		logger.Error("Failed to process knock", zap.Error(err))
		return
	}

	duration := time.Since(event.VerifiedAt)
	logger.Info("Knock processed successfully", zap.Duration("duration", duration))
}

// processKnock handles the key installation for a knock event
func (p *Processor) processKnock(ctx context.Context, logger *zap.Logger) error {
	// Get fresh metadata
	metadata, err := p.metadataClient.GetMetadataWithRetry(ctx, p.config.ResourceID, p.config.MaxRetries)
	if err != nil {
		return fmt.Errorf("failed to get metadata: %w", err)
	}

	// Update UDP listener with latest public signing key
	if metadata.PublicKey != "" {
		logger.Debug("Updating UDP listener public key")
		if err := p.udpListener.UpdatePublicKey(metadata.PublicKey); err != nil {
			logger.Warn("Failed to update public key", zap.Error(err))
		}
	}

	// Fetch console key
	consoleKey, err := p.cloudAPIClient.FetchConsoleKeyWithRetry(ctx, metadata.Token, p.config.MaxRetries)
	if err != nil {
		return fmt.Errorf("failed to fetch console key: %w", err)
	}

	// Convert to SSH key data
	keyData := &ssh.ConsoleKeyData{
		PublicKey:   consoleKey.PublicKey,
		Principal:   consoleKey.Principal,
		RequesterID: consoleKey.RequesterID,
		CreatedAt:   consoleKey.CreatedAt,
		ExpireAt:    consoleKey.ExpireAt,
	}

	// Install the key
	if err := p.keyManager.Install(keyData); err != nil {
		return fmt.Errorf("failed to install key: %w", err)
	}

	logger.Debug("Console key installed",
		zap.String("principal", keyData.Principal),
		zap.String("requester", keyData.RequesterID))

	return nil
}

// closeClients closes all client connections
func (p *Processor) closeClients() {
	if p.metadataClient != nil {
		p.metadataClient.Close()
	}
	if p.cloudAPIClient != nil {
		p.cloudAPIClient.Close()
	}
}