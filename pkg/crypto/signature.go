package crypto

import (
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"time"
)

const (
	TimestampSize      = 8
	ExpectedPacketSize = TimestampSize + ed25519.SignatureSize
)

type SignatureVerifier struct {
	publicKey ed25519.PublicKey
}

func NewSignatureVerifier(publicKeyHex string) (*SignatureVerifier, error) {
	publicKeyBytes, err := hex.DecodeString(publicKeyHex)
	if err != nil {
		return nil, fmt.Errorf("invalid public key hex: %w", err)
	}

	if len(publicKeyBytes) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid public key size: expected %d, got %d", 
			ed25519.PublicKeySize, len(publicKeyBytes))
	}

	return &SignatureVerifier{
		publicKey: ed25519.PublicKey(publicKeyBytes),
	}, nil
}

// VerifyKnockPacket verifies a packet containing timestamp
func (v *SignatureVerifier) VerifyKnockPacket(packet []byte, resourceID string, maxTimeSkew time.Duration) error {
	// Validate packet size to prevent buffer overflow
	if len(packet) != ExpectedPacketSize {
		return fmt.Errorf("invalid packet size: expected %d, got %d", ExpectedPacketSize, len(packet))
	}

	timestamp := int64(binary.BigEndian.Uint64(packet[:TimestampSize]))
	signature := packet[TimestampSize:]

	if err := v.validateTimestamp(timestamp, maxTimeSkew); err != nil {
		return err
	}

	message := fmt.Sprintf("%s|%d", resourceID, timestamp)
	if !ed25519.Verify(v.publicKey, []byte(message), signature) {
		return fmt.Errorf("signature verification failed")
	}

	return nil
}

func (v *SignatureVerifier) validateTimestamp(timestamp int64, maxTimeSkew time.Duration) error {
	now := time.Now().Unix()
	timeDiff := time.Duration(math.Abs(float64(now-timestamp))) * time.Second

	if timeDiff > maxTimeSkew {
		return fmt.Errorf("timestamp out of range: %v difference (max: %v)", 
			timeDiff, maxTimeSkew)
	}

	return nil
}