package crypto

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"testing"
	"time"
)

func TestNewSignatureVerifier(t *testing.T) {
	// Generate a test key pair
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate test key: %v", err)
	}
	
	publicKeyHex := hex.EncodeToString(publicKey)
	
	tests := []struct {
		name        string
		publicKeyHex string
		expectError bool
		errorMsg    string
	}{
		{
			name:         "valid public key",
			publicKeyHex: publicKeyHex,
			expectError:  false,
		},
		{
			name:         "invalid hex encoding",
			publicKeyHex: "invalid-hex",
			expectError:  true,
			errorMsg:     "invalid public key hex",
		},
		{
			name:         "wrong key size",
			publicKeyHex: "deadbeef",
			expectError:  true,
			errorMsg:     "invalid public key size",
		},
		{
			name:         "empty key",
			publicKeyHex: "",
			expectError:  true,
			errorMsg:     "invalid public key",
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verifier, err := NewSignatureVerifier(tt.publicKeyHex)
			
			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error but got none")
				} else if tt.errorMsg != "" && err.Error()[:len(tt.errorMsg)] != tt.errorMsg {
					t.Errorf("Expected error starting with '%s', got '%s'", tt.errorMsg, err.Error())
				}
				if verifier != nil {
					t.Errorf("Expected nil verifier on error")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if verifier == nil {
					t.Errorf("Expected verifier but got nil")
				}
			}
		})
	}
}

func TestVerifyKnockPacket(t *testing.T) {
	// Generate a test key pair
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("Failed to generate test key: %v", err)
	}
	
	publicKeyHex := hex.EncodeToString(publicKey)
	verifier, err := NewSignatureVerifier(publicKeyHex)
	if err != nil {
		t.Fatalf("Failed to create verifier: %v", err)
	}
	
	resourceID := "test-resource-123"
	maxTimeSkew := 30 * time.Second
	
	// Helper function to create a valid packet
	createValidPacket := func(timestamp int64) []byte {
		message := fmt.Sprintf("%s|%d", resourceID, timestamp)
		signature := ed25519.Sign(privateKey, []byte(message))
		
		packet := make([]byte, TimestampSize+len(signature))
		binary.BigEndian.PutUint64(packet[:TimestampSize], uint64(timestamp))
		copy(packet[TimestampSize:], signature)
		
		return packet
	}
	
	tests := []struct {
		name         string
		packetFunc   func() []byte
		resourceID   string
		maxTimeSkew  time.Duration
		expectError  bool
		errorMsg     string
	}{
		{
			name: "valid packet with current timestamp",
			packetFunc: func() []byte {
				return createValidPacket(time.Now().Unix())
			},
			resourceID:  resourceID,
			maxTimeSkew: maxTimeSkew,
			expectError: false,
		},
		{
			name: "packet with timestamp in near past",
			packetFunc: func() []byte {
				return createValidPacket(time.Now().Add(-10 * time.Second).Unix())
			},
			resourceID:  resourceID,
			maxTimeSkew: maxTimeSkew,
			expectError: false,
		},
		{
			name: "packet with timestamp in near future",
			packetFunc: func() []byte {
				return createValidPacket(time.Now().Add(10 * time.Second).Unix())
			},
			resourceID:  resourceID,
			maxTimeSkew: maxTimeSkew,
			expectError: false,
		},
		{
			name: "packet with timestamp too old",
			packetFunc: func() []byte {
				return createValidPacket(time.Now().Add(-60 * time.Second).Unix())
			},
			resourceID:  resourceID,
			maxTimeSkew: maxTimeSkew,
			expectError: true,
			errorMsg:    "timestamp out of range",
		},
		{
			name: "packet with timestamp too far in future",
			packetFunc: func() []byte {
				return createValidPacket(time.Now().Add(60 * time.Second).Unix())
			},
			resourceID:  resourceID,
			maxTimeSkew: maxTimeSkew,
			expectError: true,
			errorMsg:    "timestamp out of range",
		},
		{
			name: "packet with wrong resource ID",
			packetFunc: func() []byte {
				return createValidPacket(time.Now().Unix())
			},
			resourceID:  "wrong-resource-id",
			maxTimeSkew: maxTimeSkew,
			expectError: true,
			errorMsg:    "signature verification failed",
		},
		{
			name: "packet with corrupted signature",
			packetFunc: func() []byte {
				packet := createValidPacket(time.Now().Unix())
				// Corrupt the signature
				packet[TimestampSize] ^= 0xFF
				return packet
			},
			resourceID:  resourceID,
			maxTimeSkew: maxTimeSkew,
			expectError: true,
			errorMsg:    "signature verification failed",
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			packet := tt.packetFunc()
			err := verifier.VerifyKnockPacket(packet, tt.resourceID, tt.maxTimeSkew)
			
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
			}
		})
	}
}

func TestValidateTimestamp(t *testing.T) {
	verifier := &SignatureVerifier{} // Don't need a real key for timestamp validation
	
	now := time.Now().Unix()
	maxTimeSkew := 30 * time.Second
	
	tests := []struct {
		name        string
		timestamp   int64
		maxTimeSkew time.Duration
		expectError bool
	}{
		{
			name:        "current timestamp",
			timestamp:   now,
			maxTimeSkew: maxTimeSkew,
			expectError: false,
		},
		{
			name:        "timestamp 10 seconds ago",
			timestamp:   now - 10,
			maxTimeSkew: maxTimeSkew,
			expectError: false,
		},
		{
			name:        "timestamp 10 seconds in future",
			timestamp:   now + 10,
			maxTimeSkew: maxTimeSkew,
			expectError: false,
		},
		{
			name:        "timestamp exactly at max skew",
			timestamp:   now - 30,
			maxTimeSkew: maxTimeSkew,
			expectError: false,
		},
		{
			name:        "timestamp beyond max skew",
			timestamp:   now - 60,
			maxTimeSkew: maxTimeSkew,
			expectError: true,
		},
		{
			name:        "timestamp too far in future",
			timestamp:   now + 60,
			maxTimeSkew: maxTimeSkew,
			expectError: true,
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := verifier.validateTimestamp(tt.timestamp, tt.maxTimeSkew)
			
			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error but got none")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
			}
		})
	}
}

func TestConstants(t *testing.T) {
	if TimestampSize != 8 {
		t.Errorf("Expected TimestampSize to be 8, got %d", TimestampSize)
	}
	
	if ExpectedPacketSize != TimestampSize+ed25519.SignatureSize {
		t.Errorf("Expected ExpectedPacketSize to be %d, got %d", 
			TimestampSize+ed25519.SignatureSize, ExpectedPacketSize)
	}
}

// Benchmark tests
func BenchmarkNewSignatureVerifier(b *testing.B) {
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		b.Fatalf("Failed to generate test key: %v", err)
	}
	publicKeyHex := hex.EncodeToString(publicKey)
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := NewSignatureVerifier(publicKeyHex)
		if err != nil {
			b.Fatalf("Unexpected error: %v", err)
		}
	}
}

func BenchmarkVerifyKnockPacket(b *testing.B) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		b.Fatalf("Failed to generate test key: %v", err)
	}
	
	publicKeyHex := hex.EncodeToString(publicKey)
	verifier, err := NewSignatureVerifier(publicKeyHex)
	if err != nil {
		b.Fatalf("Failed to create verifier: %v", err)
	}
	
	resourceID := "test-resource-123"
	timestamp := time.Now().Unix()
	message := fmt.Sprintf("%s|%d", resourceID, timestamp)
	signature := ed25519.Sign(privateKey, []byte(message))
	
	packet := make([]byte, TimestampSize+len(signature))
	binary.BigEndian.PutUint64(packet[:TimestampSize], uint64(timestamp))
	copy(packet[TimestampSize:], signature)
	
	maxTimeSkew := 30 * time.Second
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		err := verifier.VerifyKnockPacket(packet, resourceID, maxTimeSkew)
		if err != nil {
			b.Fatalf("Unexpected error: %v", err)
		}
	}
}