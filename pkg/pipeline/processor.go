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

func NewProcessor(cfg *config.Config, logger *zap.Logger) (*Processor, error) {
	metadataClient := metadata.NewClient(cfg.MetadataServiceEndpoint, cfg.HTTPTimeout, logger)
	cloudAPIClient := cloudapi.NewClient(metadataClient, cfg.ResourceID, cfg.HTTPTimeout, logger)

	keyManager, err := ssh.NewKeyManager(logger)
	if err != nil {
		return nil, err
	}

	udpListener, err := console.NewUDPListener(cfg, logger)
	if err != nil {
		return nil, err
	}

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

func (p *Processor) Start() error {
	p.logger.Info("Starting console access processor")

	p.logger.Info("Fetching initial public key from metadata service: Testing auto update")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, publicKey, _, err := p.metadataClient.GetAuthToken(ctx, p.config.ResourceID)
	if err != nil {
		p.logger.Error("Failed to fetch initial public key", zap.Error(err))
		return fmt.Errorf("failed to initialize public key: %w", err)
	}

	if publicKey != "" {
		if err := p.udpListener.UpdatePublicKey(publicKey); err != nil {
			return fmt.Errorf("failed to set initial public key: %w", err)
		}
		p.logger.Info("Initialized UDP listener with public key from metadata service")
	} else {
		return fmt.Errorf("metadata service did not provide public key")
	}

	if err := p.udpListener.Start(); err != nil {
		return err
	}

	p.startBackgroundJobs()

	p.wg.Add(1)
	go p.processLoop()

	return nil
}

func (p *Processor) Stop() error {
	p.logger.Info("Stopping console access processor")

	p.cancel()

	if err := p.udpListener.Stop(); err != nil {
		p.logger.Error("Error stopping UDP listener", zap.Error(err))
	}

	if p.cleanupTicker != nil {
		p.cleanupTicker.Stop()
	}

	p.wg.Wait()

	if p.metadataClient != nil {
		p.metadataClient.Close()
	}
	if p.cloudAPIClient != nil {
		p.cloudAPIClient.Close()
	}

	return nil
}

func (p *Processor) startBackgroundJobs() {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()

		for {
			select {
			case <-p.ctx.Done():
				return
			case <-p.cleanupTicker.C:
				p.performKeyCleanup()
			}
		}
	}()
}

func (p *Processor) processLoop() {
	defer p.wg.Done()

	eventChan := p.udpListener.GetEventChannel()

	for {
		select {
		case <-p.ctx.Done():
			return
		case event, ok := <-eventChan:
			if !ok {
				return
			}
			p.handleKnockEvent(event)
		}
	}
}

func (p *Processor) handleKnockEvent(event console.UDPKnockEvent) {
	p.logger.Info("Processing UDP knock",
		zap.String("source_ip", event.SourceIP),
		zap.Time("knock_time", time.Unix(event.Timestamp, 0)))

	ctx, cancel := context.WithTimeout(p.ctx, 30*time.Second)
	defer cancel()

	// Get auth token and public key from metadata service
	authToken, publicKey, _, err := p.metadataClient.GetAuthToken(ctx, p.config.ResourceID)
	if err != nil {
		p.logger.Error("Failed to get auth token", zap.Error(err))
		return
	}

	// Update UDP listener with public key
	if publicKey != "" {
		p.logger.Debug("Updating UDP listener with public key from metadata service",
			zap.String("public_key", publicKey))

		if updateErr := p.udpListener.UpdatePublicKey(publicKey); updateErr != nil {
			p.logger.Error("Failed to update UDP listener public key", zap.Error(updateErr))
		}
	}

	// Fetch Strettch Access Token (SAT) key
	satKey, err := p.cloudAPIClient.GetSATKeyWithRetry(ctx, authToken, p.config.MaxRetries, p.config.RetryInterval)
	if err != nil {
		p.logger.Error("Failed to fetch SAT key", zap.Error(err))
		return
	}

	// Install SAT key
	if err := p.keyManager.InstallSATKey(satKey); err != nil {
		p.logger.Error("Failed to install SAT key", zap.Error(err))
		return
	}

	p.logger.Info("Knock processed successfully",
		zap.String("source_ip", event.SourceIP),
		zap.Duration("duration", time.Since(event.VerifiedAt)))
}

func (p *Processor) performKeyCleanup() {
	activeKeys, _ := p.keyManager.GetActiveSATKeys()
	beforeCount := len(activeKeys)

	if err := p.keyManager.CleanupExpiredKeys(); err != nil {
		p.logger.Error("SSH key cleanup failed", zap.Error(err))
		return
	}

	remainingKeys, _ := p.keyManager.GetActiveSATKeys()
	afterCount := len(remainingKeys)

	if removed := beforeCount - afterCount; removed > 0 {
		p.logger.Debug("SSH keys cleaned up",
			zap.Int("removed", removed),
			zap.Int("remaining", afterCount))
	}
}
