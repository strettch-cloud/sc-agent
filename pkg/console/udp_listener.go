package console

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/strettch-cloud/sc-agent/pkg/config"
	"github.com/strettch-cloud/sc-agent/pkg/crypto"
	"go.uber.org/zap"
)

const (
	eventChannelBuffer = 10
	udpReadTimeout     = 1 * time.Second
	minPacketSize      = 8 + 64 // timestamp + Ed25519 signature
)

type UDPKnockEvent struct {
	SourceIP   string
	Timestamp  int64
	VerifiedAt time.Time
}

type UDPListener struct {
	config    *config.Config
	logger    *zap.Logger
	verifier  *crypto.SignatureVerifier
	conn      *net.UDPConn
	eventChan chan UDPKnockEvent

	ctx    context.Context
	cancel context.CancelFunc

	publicKeyHex string
	mu           sync.RWMutex
}

func NewUDPListener(cfg *config.Config, logger *zap.Logger) *UDPListener {
	ctx, cancel := context.WithCancel(context.Background())

	return &UDPListener{
		config:       cfg,
		logger:       logger,
		verifier:     nil,
		eventChan:    make(chan UDPKnockEvent, eventChannelBuffer),
		ctx:          ctx,
		cancel:       cancel,
		publicKeyHex: "",
	}
}

func (ul *UDPListener) Start() error {
	addr := &net.UDPAddr{Port: ul.config.Console.UDPKnockPort}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on UDP port %d: %w", ul.config.Console.UDPKnockPort, err)
	}
	ul.conn = conn

	ul.logger.Info("UDP knock listener started",
		zap.String("address", addr.String()),
		zap.String("resource_id", ul.config.ResourceID))

	go ul.processPackets()
	return nil
}

func (ul *UDPListener) Stop() {
	ul.logger.Info("Stopping UDP knock listener")
	ul.cancel()

	if ul.conn != nil {
		// Closing fails only for a socket that's already closed.
		_ = ul.conn.Close()
	}

	// The event channel stays open: the packet goroutine may still be handing
	// on a knock it read before Stop, and sending on a closed channel panics.
	// The processor stops reading it when its own context ends.
}

func (ul *UDPListener) GetEventChannel() <-chan UDPKnockEvent {
	return ul.eventChan
}

func (ul *UDPListener) processPackets() {
	buffer := make([]byte, ul.config.Console.MaxPacketSize)

	for {
		select {
		case <-ul.ctx.Done():
			return
		default:
		}

		// Timeout allows graceful shutdown
		if err := ul.conn.SetReadDeadline(time.Now().Add(udpReadTimeout)); err != nil {
			ul.logger.Warn("Failed to set read deadline", zap.Error(err))
		}

		n, addr, err := ul.conn.ReadFromUDP(buffer)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}

			select {
			case <-ul.ctx.Done():
				return
			default:
				ul.logger.Error("Failed to read UDP packet", zap.Error(err))
				continue
			}
		}

		ul.handlePacket(buffer[:n], addr.IP.String())
	}
}

func (ul *UDPListener) handlePacket(packet []byte, sourceIP string) {
	if len(packet) < minPacketSize {
		ul.logger.Debug("Packet too small",
			zap.String("source_ip", sourceIP),
			zap.Int("size", len(packet)),
			zap.Int("min_size", minPacketSize))
		return
	}

	// Check if verifier is initialized
	ul.mu.RLock()
	verifier := ul.verifier
	ul.mu.RUnlock()

	if verifier == nil {
		ul.logger.Debug("No public key configured yet, ignoring packet",
			zap.String("source_ip", sourceIP))
		return
	}

	signatureTimeout := time.Duration(ul.config.Console.SignatureTimeout) * time.Second
	if err := verifier.VerifyKnockPacket(packet, ul.config.ResourceID, signatureTimeout); err != nil {
		ul.logger.Debug("Invalid knock signature",
			zap.String("source_ip", sourceIP),
			zap.Error(err))
		return
	}

	timestamp := int64(binary.BigEndian.Uint64(packet[:8]))

	ul.logger.Info("Valid UDP knock received",
		zap.String("source_ip", sourceIP),
		zap.Time("knock_time", time.Unix(timestamp, 0)))

	event := UDPKnockEvent{
		SourceIP:   sourceIP,
		Timestamp:  timestamp,
		VerifiedAt: time.Now(),
	}

	select {
	case ul.eventChan <- event:
	default:
		ul.logger.Warn("Event channel full, dropping knock",
			zap.String("source_ip", sourceIP))
	}
}

// UpdatePublicKey updates public key used for signature verification
func (ul *UDPListener) UpdatePublicKey(publicKeyHex string) error {
	if publicKeyHex == "" {
		return fmt.Errorf("empty public key")
	}

	ul.mu.Lock()
	defer ul.mu.Unlock()

	// Only update if key has changed
	if ul.publicKeyHex == publicKeyHex {
		return nil
	}

	verifier, err := crypto.NewSignatureVerifier(publicKeyHex)
	if err != nil {
		return fmt.Errorf("failed to create verifier with new key: %w", err)
	}

	ul.verifier = verifier
	ul.publicKeyHex = publicKeyHex
	ul.logger.Info("Updated UDP knock public key",
		zap.String("public_key_hex", publicKeyHex))

	return nil
}
