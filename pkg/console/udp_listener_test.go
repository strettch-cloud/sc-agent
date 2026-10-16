package console

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/strettch-cloud/sc-agent/pkg/config"
	"go.uber.org/zap"
)

func TestNewUDPListener(t *testing.T) {
	cfg := &config.Config{
		ResourceID: "test-resource",
		Console: config.ConsoleConfig{
			UDPKnockPort:     60124,
			MaxPacketSize:    1024,
			SignatureTimeout: 30,
		},
	}

	logger := zap.NewNop()

	listener, err := NewUDPListener(cfg, logger)
	if err != nil {
		t.Fatalf("Failed to create UDP listener: %v", err)
	}

	if listener == nil {
		t.Fatal("Expected non-nil listener")
	}

	if listener.config != cfg {
		t.Error("Config not properly set")
	}

	if listener.logger != logger {
		t.Error("Logger not properly set")
	}

	if listener.verifier != nil {
		t.Error("Verifier should be nil initially")
	}

	if listener.eventChan == nil {
		t.Error("Event channel should be initialized")
	}
}

func TestUDPListenerStartStop(t *testing.T) {
	cfg := &config.Config{
		ResourceID: "test-resource",
		Console: config.ConsoleConfig{
			UDPKnockPort:     0, // Let OS choose port
			MaxPacketSize:    1024,
			SignatureTimeout: 30,
		},
	}

	logger := zap.NewNop()

	listener, err := NewUDPListener(cfg, logger)
	if err != nil {
		t.Fatalf("Failed to create UDP listener: %v", err)
	}

	// Start listener
	err = listener.Start()
	if err != nil {
		t.Fatalf("Failed to start UDP listener: %v", err)
	}

	// Verify connection is established
	if listener.conn == nil {
		t.Error("Connection should be established after start")
	}

	// Stop listener
	err = listener.Stop()
	if err != nil {
		t.Errorf("Failed to stop UDP listener: %v", err)
	}
}

func TestUpdatePublicKey(t *testing.T) {
	cfg := &config.Config{
		ResourceID: "test-resource",
		Console: config.ConsoleConfig{
			UDPKnockPort:     60124,
			MaxPacketSize:    1024,
			SignatureTimeout: 30,
		},
	}

	logger := zap.NewNop()

	listener, err := NewUDPListener(cfg, logger)
	if err != nil {
		t.Fatalf("Failed to create UDP listener: %v", err)
	}

	// Generate test key
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate test key: %v", err)
	}
	publicKeyHex := hex.EncodeToString(publicKey)

	tests := []struct {
		name         string
		publicKeyHex string
		expectError  bool
		errorMsg     string
	}{
		{
			name:         "valid public key",
			publicKeyHex: publicKeyHex,
			expectError:  false,
		},
		{
			name:         "empty public key",
			publicKeyHex: "",
			expectError:  true,
			errorMsg:     "empty public key",
		},
		{
			name:         "invalid hex",
			publicKeyHex: "invalid-hex",
			expectError:  true,
			errorMsg:     "failed to create verifier",
		},
		{
			name:         "same key twice",
			publicKeyHex: publicKeyHex,
			expectError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := listener.UpdatePublicKey(tt.publicKeyHex)

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error but got none")
				} else if tt.errorMsg != "" && err.Error()[:len(tt.errorMsg)] != tt.errorMsg {
					t.Errorf("Expected error starting with '%s', got '%s'", tt.errorMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}

				listener.mu.RLock()
				hasVerifier := listener.verifier != nil
				keyMatches := listener.publicKeyHex == tt.publicKeyHex
				listener.mu.RUnlock()

				if !hasVerifier {
					t.Error("Verifier should be set after successful key update")
				}

				if !keyMatches {
					t.Error("Public key hex should be stored")
				}
			}
		})
	}
}

func TestGetEventChannel(t *testing.T) {
	cfg := &config.Config{
		ResourceID: "test-resource",
		Console: config.ConsoleConfig{
			UDPKnockPort:     60124,
			MaxPacketSize:    1024,
			SignatureTimeout: 30,
		},
	}

	logger := zap.NewNop()

	listener, err := NewUDPListener(cfg, logger)
	if err != nil {
		t.Fatalf("Failed to create UDP listener: %v", err)
	}

	eventChan := listener.GetEventChannel()
	if eventChan == nil {
		t.Error("Event channel should not be nil")
	}

	// Check that it's the same channel
	if eventChan != listener.eventChan {
		t.Error("Returned channel should be the same as internal channel")
	}
}

func TestHandlePacket(t *testing.T) {
	cfg := &config.Config{
		ResourceID: "test-resource-123",
		Console: config.ConsoleConfig{
			UDPKnockPort:     60124,
			MaxPacketSize:    1024,
			SignatureTimeout: 30,
		},
	}

	logger := zap.NewNop()

	listener, err := NewUDPListener(cfg, logger)
	if err != nil {
		t.Fatalf("Failed to create UDP listener: %v", err)
	}

	// Generate test key pair
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate test key: %v", err)
	}
	publicKeyHex := hex.EncodeToString(publicKey)

	// Set up public key
	err = listener.UpdatePublicKey(publicKeyHex)
	if err != nil {
		t.Fatalf("Failed to update public key: %v", err)
	}

	// Helper function to create a valid packet
	createValidPacket := func(resourceID string, timestamp int64) []byte {
		message := fmt.Sprintf("%s|%d", resourceID, timestamp)
		signature := ed25519.Sign(privateKey, []byte(message))

		packet := make([]byte, 8+len(signature))
		binary.BigEndian.PutUint64(packet[:8], uint64(timestamp))
		copy(packet[8:], signature)

		return packet
	}

	tests := []struct {
		name          string
		packetFunc    func() []byte
		sourceIP      string
		expectEvent   bool
		setupVerifier bool
	}{
		{
			name: "valid packet",
			packetFunc: func() []byte {
				return createValidPacket("test-resource-123", time.Now().Unix())
			},
			sourceIP:      "192.168.1.100",
			expectEvent:   true,
			setupVerifier: true,
		},
		{
			name: "packet too small",
			packetFunc: func() []byte {
				return make([]byte, minPacketSize-1)
			},
			sourceIP:      "192.168.1.100",
			expectEvent:   false,
			setupVerifier: true,
		},
		{
			name: "no verifier configured",
			packetFunc: func() []byte {
				return createValidPacket("test-resource-123", time.Now().Unix())
			},
			sourceIP:      "192.168.1.100",
			expectEvent:   false,
			setupVerifier: false,
		},
		{
			name: "invalid signature",
			packetFunc: func() []byte {
				packet := createValidPacket("wrong-resource", time.Now().Unix())
				return packet
			},
			sourceIP:      "192.168.1.100",
			expectEvent:   false,
			setupVerifier: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset verifier state
			if tt.setupVerifier {
				err = listener.UpdatePublicKey(publicKeyHex)
				if err != nil {
					t.Fatalf("Failed to setup verifier: %v", err)
				}
			} else {
				listener.mu.Lock()
				listener.verifier = nil
				listener.mu.Unlock()
			}

			// Clear any existing events
			select {
			case <-listener.eventChan:
			default:
			}

			packet := tt.packetFunc()
			listener.handlePacket(packet, tt.sourceIP)

			// Check if event was generated
			select {
			case event := <-listener.eventChan:
				if !tt.expectEvent {
					t.Errorf("Unexpected event received: %+v", event)
				} else {
					if event.SourceIP != tt.sourceIP {
						t.Errorf("Expected source IP %s, got %s", tt.sourceIP, event.SourceIP)
					}
				}
			case <-time.After(100 * time.Millisecond):
				if tt.expectEvent {
					t.Error("Expected event but none received")
				}
			}
		})
	}
}

func TestUDPKnockEvent(t *testing.T) {
	event := UDPKnockEvent{
		SourceIP:   "192.168.1.100",
		Timestamp:  time.Now().Unix(),
		VerifiedAt: time.Now(),
	}

	if event.SourceIP != "192.168.1.100" {
		t.Errorf("Expected source IP 192.168.1.100, got %s", event.SourceIP)
	}

	if event.Timestamp == 0 {
		t.Error("Timestamp should not be zero")
	}

	if event.VerifiedAt.IsZero() {
		t.Error("VerifiedAt should not be zero")
	}
}

func TestConstants(t *testing.T) {
	if eventChannelBuffer != 10 {
		t.Errorf("Expected eventChannelBuffer to be 10, got %d", eventChannelBuffer)
	}

	if udpReadTimeout != 1*time.Second {
		t.Errorf("Expected udpReadTimeout to be 1s, got %v", udpReadTimeout)
	}

	if minPacketSize != 8+64 {
		t.Errorf("Expected minPacketSize to be 72, got %d", minPacketSize)
	}
}

// Integration test that simulates real UDP communication
func TestUDPListenerIntegration(t *testing.T) {
	cfg := &config.Config{
		ResourceID: "test-resource-123",
		Console: config.ConsoleConfig{
			UDPKnockPort:     0, // Let OS choose port
			MaxPacketSize:    1024,
			SignatureTimeout: 30,
		},
	}

	logger := zap.NewNop()

	listener, err := NewUDPListener(cfg, logger)
	if err != nil {
		t.Fatalf("Failed to create UDP listener: %v", err)
	}

	// Generate test key pair
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate test key: %v", err)
	}
	publicKeyHex := hex.EncodeToString(publicKey)

	// Set up public key
	err = listener.UpdatePublicKey(publicKeyHex)
	if err != nil {
		t.Fatalf("Failed to update public key: %v", err)
	}

	// Start listener
	err = listener.Start()
	if err != nil {
		t.Fatalf("Failed to start listener: %v", err)
	}
	defer func() {
		if err := listener.Stop(); err != nil {
			t.Logf("Failed to stop listener: %v", err)
		}
	}()

	// Get the actual port that was assigned
	addr := listener.conn.LocalAddr().(*net.UDPAddr)
	port := addr.Port

	// Create a valid packet
	timestamp := time.Now().Unix()
	message := fmt.Sprintf("%s|%d", cfg.ResourceID, timestamp)
	signature := ed25519.Sign(privateKey, []byte(message))

	packet := make([]byte, 8+len(signature))
	binary.BigEndian.PutUint64(packet[:8], uint64(timestamp))
	copy(packet[8:], signature)

	// Send packet
	clientConn, err := net.Dial("udp", fmt.Sprintf("localhost:%d", port))
	if err != nil {
		t.Fatalf("Failed to create client connection: %v", err)
	}
	defer func() {
		if err := clientConn.Close(); err != nil {
			t.Logf("Failed to close client connection: %v", err)
		}
	}()

	_, err = clientConn.Write(packet)
	if err != nil {
		t.Fatalf("Failed to send packet: %v", err)
	}

	// Wait for event
	select {
	case event := <-listener.GetEventChannel():
		if event.SourceIP == "" {
			t.Error("Source IP should not be empty")
		}
		if event.Timestamp != timestamp {
			t.Errorf("Expected timestamp %d, got %d", timestamp, event.Timestamp)
		}
		if event.VerifiedAt.IsZero() {
			t.Error("VerifiedAt should not be zero")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Expected event but none received within timeout")
	}
}
