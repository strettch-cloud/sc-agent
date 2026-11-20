package metadata

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestNewClient(t *testing.T) {
	endpoint := "http://example.com/metadata"
	timeout := 30 * time.Second
	logger := zap.NewNop()
	
	client := NewClient(endpoint, timeout, logger)
	
	if client == nil {
		t.Fatal("Expected non-nil client")
	}
	
	if client.endpoint != endpoint {
		t.Errorf("Expected endpoint %s, got %s", endpoint, client.endpoint)
	}
	
	if client.logger != logger {
		t.Error("Logger not properly set")
	}
	
	if client.httpClient.Timeout != timeout {
		t.Errorf("Expected timeout %v, got %v", timeout, client.httpClient.Timeout)
	}
}

func TestContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate slow response
		time.Sleep(100 * time.Millisecond)
		response := Response{
			Token:       "test-token",
			CloudAPIUrl: "https://api.example.com",
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			// Error encoding response in test
			_ = err // Suppress staticcheck SA9003
		}
	}))
	defer server.Close()
	
	client := NewClient(server.URL, 10*time.Second, zap.NewNop())
	
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	
	_, err := client.GetMetadata(ctx)
	if err == nil {
		t.Error("Expected timeout error")
	}
	
	if !strings.Contains(err.Error(), "context") {
		t.Errorf("Expected context-related error, got: %v", err)
	}
}

func TestClose(t *testing.T) {
	client := NewClient("http://example.com", 10*time.Second, zap.NewNop())
	
	// This should not panic
	client.Close()
}

func TestConstants(t *testing.T) {
	if HeaderAccept != "Accept" {
		t.Errorf("Expected HeaderAccept to be 'Accept', got '%s'", HeaderAccept)
	}
	
	if AcceptJSON != "application/json" {
		t.Errorf("Expected AcceptJSON to be 'application/json', got '%s'", AcceptJSON)
	}
}

func TestResponseStruct(t *testing.T) {
	response := Response{
		Token:       "test-token",
		PublicKey:   "test-key",
		CloudAPIUrl: "https://api.example.com",
	}
	
	if response.Token != "test-token" {
		t.Errorf("Expected token test-token, got %s", response.Token)
	}
	
	if response.PublicKey != "test-key" {
		t.Error("PublicKey not properly set")
	}
	
	if response.CloudAPIUrl != "https://api.example.com" {
		t.Errorf("Expected CloudAPIUrl https://api.example.com, got %s", response.CloudAPIUrl)
	}
}

func TestInfoStruct(t *testing.T) {
	info := Info{
		Token:       "test-token",
		PublicKey:   "test-key",
		CloudAPIURL: "https://api.example.com",
	}
	
	if info.Token != "test-token" {
		t.Errorf("Expected token test-token, got %s", info.Token)
	}
	
	if info.PublicKey != "test-key" {
		t.Errorf("Expected public key test-key, got %s", info.PublicKey)
	}
	
	if info.CloudAPIURL != "https://api.example.com" {
		t.Errorf("Expected CloudAPIURL https://api.example.com, got %s", info.CloudAPIURL)
	}
}

// Benchmark tests
func BenchmarkGetMetadata(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := Response{
			Token:       "bench-token",
			ExpiresAt:   time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			PublicKey:   "bench-key",
			CloudAPIUrl: "https://api.example.com",
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			// Error encoding response in test
			_ = err // Suppress staticcheck SA9003
		}
	}))
	defer server.Close()
	
	client := NewClient(server.URL, 10*time.Second, zap.NewNop())
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := client.GetMetadata(context.Background())
		if err != nil {
			b.Fatalf("Unexpected error: %v", err)
		}
	}
}

func BenchmarkGetMetadataCached(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := Response{
			Token:       "bench-token",
			ExpiresAt:   time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			PublicKey:   "bench-key",
			CloudAPIUrl: "https://api.example.com",
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			// Error encoding response in test
			_ = err // Suppress staticcheck SA9003
		}
	}))
	defer server.Close()
	
	client := NewClient(server.URL, 10*time.Second, zap.NewNop())
	
	// Prime the cache
	_, err := client.GetMetadata(context.Background())
	if err != nil {
		b.Fatalf("Failed to prime cache: %v", err)
	}
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := client.GetMetadata(context.Background())
		if err != nil {
			b.Fatalf("Unexpected error: %v", err)
		}
	}
}