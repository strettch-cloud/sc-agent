package metadata

import (
	"context"
	"encoding/json"
	"fmt"
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
	
	if client.cacheDuration != defaultCacheDuration {
		t.Errorf("Expected cache duration %v, got %v", defaultCacheDuration, client.cacheDuration)
	}
}

func TestGetMetadata(t *testing.T) {
	tests := []struct {
		name           string
		serverResponse func(w http.ResponseWriter, r *http.Request)
		resourceID     string
		expectError    bool
		expectedInfo   *Info
	}{
		{
			name: "successful response",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				// Verify request headers
				if r.Header.Get(HeaderAccept) != AcceptJSON {
					t.Errorf("Expected Accept header %s, got %s", AcceptJSON, r.Header.Get(HeaderAccept))
				}
				
				if r.Header.Get(HeaderXResourceID) != "test-resource" {
					t.Errorf("Expected X-Resource-ID header test-resource, got %s", r.Header.Get(HeaderXResourceID))
				}
				
				// Verify query parameter
				if !strings.Contains(r.URL.RawQuery, "includePublicKey=True") {
					t.Error("Expected includePublicKey=True in query parameters")
				}
				
				response := Response{
					Token:       "test-token-123",
					PublicKey:   stringPtr("abcdef123456"),
					CloudAPIUrl: "https://api.example.com",
				}
				
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(response); err != nil {
					// Error encoding response in test
					_ = err // Suppress staticcheck SA9003
				}
			},
			resourceID:  "test-resource",
			expectError: false,
			expectedInfo: &Info{
				Token:       "test-token-123",
				PublicKey:   "abcdef123456",
				CloudAPIURL: "https://api.example.com",
			},
		},
		{
			name: "response without public key",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				response := Response{
					Token:       "test-token-456",
					PublicKey:   nil,
					CloudAPIUrl: "https://api.example.com",
				}
				
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(response); err != nil {
					// Error encoding response in test
					_ = err // Suppress staticcheck SA9003
				}
			},
			resourceID:  "test-resource",
			expectError: false,
			expectedInfo: &Info{
				Token:       "test-token-456",
				PublicKey:   "",
				CloudAPIURL: "https://api.example.com",
			},
		},
		{
			name: "server error",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				if _, err := w.Write([]byte("Internal Server Error")); err != nil {
					// Error writing response in test
					_ = err // Suppress staticcheck SA9003
				}
			},
			resourceID:  "test-resource",
			expectError: true,
		},
		{
			name: "empty token",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				response := Response{
					Token:       "",
					CloudAPIUrl: "https://api.example.com",
				}
				
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(response); err != nil {
					// Error encoding response in test
					_ = err // Suppress staticcheck SA9003
				}
			},
			resourceID:  "test-resource",
			expectError: true,
		},
		{
			name: "invalid JSON",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if _, err := w.Write([]byte("invalid json")); err != nil {
					// Error writing response in test
					_ = err // Suppress staticcheck SA9003
				}
			},
			resourceID:  "test-resource",
			expectError: true,
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(tt.serverResponse))
			defer server.Close()
			
			client := NewClient(server.URL, 10*time.Second, zap.NewNop())
			
			info, err := client.GetMetadata(context.Background(), tt.resourceID)
			
			if tt.expectError {
				if err == nil {
					t.Error("Expected error but got none")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				
				if info == nil {
					t.Fatal("Expected info but got nil")
				}
				
				if info.Token != tt.expectedInfo.Token {
					t.Errorf("Expected token %s, got %s", tt.expectedInfo.Token, info.Token)
				}
				
				if info.PublicKey != tt.expectedInfo.PublicKey {
					t.Errorf("Expected public key %s, got %s", tt.expectedInfo.PublicKey, info.PublicKey)
				}
				
				if info.CloudAPIURL != tt.expectedInfo.CloudAPIURL {
					t.Errorf("Expected cloud API URL %s, got %s", tt.expectedInfo.CloudAPIURL, info.CloudAPIURL)
				}
			}
		})
	}
}

func TestCaching(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		response := Response{
			Token:       fmt.Sprintf("token-%d", callCount),
			PublicKey:   stringPtr("test-key"),
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
	
	// First call - should hit server
	info1, err := client.GetMetadata(context.Background(), "test-resource")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	
	if callCount != 1 {
		t.Errorf("Expected 1 server call, got %d", callCount)
	}
	
	if info1.Token != "token-1" {
		t.Errorf("Expected token-1, got %s", info1.Token)
	}
	
	// Second call - should use cache
	info2, err := client.GetMetadata(context.Background(), "test-resource")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	
	if callCount != 1 {
		t.Errorf("Expected still 1 server call (cached), got %d", callCount)
	}
	
	if info2.Token != "token-1" {
		t.Errorf("Expected cached token-1, got %s", info2.Token)
	}
}

func TestCacheExpiration(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		response := Response{
			Token:       fmt.Sprintf("token-%d", callCount),
			PublicKey:   stringPtr("test-key"),
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
	
	// Set a very short cache duration
	client.cacheDuration = time.Millisecond
	
	// First call
	info1, err := client.GetMetadata(context.Background(), "test-resource")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	
	if callCount != 1 {
		t.Errorf("Expected 1 server call, got %d", callCount)
	}
	
	// Wait for cache to expire
	time.Sleep(2 * time.Millisecond)
	
	// Second call - should hit server again
	info2, err := client.GetMetadata(context.Background(), "test-resource")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	
	if callCount != 2 {
		t.Errorf("Expected 2 server calls after cache expiration, got %d", callCount)
	}
	
	if info1.Token == info2.Token {
		t.Error("Expected different tokens after cache expiration")
	}
}

func TestGetMetadataWithRetry(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		
		response := Response{
			Token:       "success-token",
			PublicKey:   stringPtr("test-key"),
			CloudAPIUrl: "https://api.example.com",
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			// Error encoding response in test
			_ = err // Suppress staticcheck SA9003
		}
	}))
	defer server.Close()
	
	client := NewClient(server.URL, 1*time.Second, zap.NewNop())
	
	info, err := client.GetMetadataWithRetry(context.Background(), "test-resource", 5)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	
	if info.Token != "success-token" {
		t.Errorf("Expected success-token, got %s", info.Token)
	}
	
	if attempts != 3 {
		t.Errorf("Expected 3 attempts, got %d", attempts)
	}
}

func TestGetMetadataForStartup(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		
		response := Response{
			Token:       "startup-token",
			PublicKey:   stringPtr("startup-key"),
			CloudAPIUrl: "https://api.example.com",
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			// Error encoding response in test
			_ = err // Suppress staticcheck SA9003
		}
	}))
	defer server.Close()
	
	client := NewClient(server.URL, 50*time.Millisecond, zap.NewNop())
	
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	
	info, err := client.GetMetadataForStartup(ctx, "test-resource")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	
	if info.Token != "startup-token" {
		t.Errorf("Expected startup-token, got %s", info.Token)
	}
	
	if attempts < 2 {
		t.Errorf("Expected at least 2 attempts, got %d", attempts)
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
	
	_, err := client.GetMetadata(ctx, "test-resource")
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
	
	if HeaderXResourceID != "X-Resource-ID" {
		t.Errorf("Expected HeaderXResourceID to be 'X-Resource-ID', got '%s'", HeaderXResourceID)
	}
	
	if AcceptJSON != "application/json" {
		t.Errorf("Expected AcceptJSON to be 'application/json', got '%s'", AcceptJSON)
	}
	
	if defaultCacheDuration != 30*time.Minute {
		t.Errorf("Expected defaultCacheDuration to be 30m, got %v", defaultCacheDuration)
	}
}

func TestResponseStruct(t *testing.T) {
	response := Response{
		Token:       "test-token",
		PublicKey:   stringPtr("test-key"),
		CloudAPIUrl: "https://api.example.com",
	}
	
	if response.Token != "test-token" {
		t.Errorf("Expected token test-token, got %s", response.Token)
	}
	
	if response.PublicKey == nil || *response.PublicKey != "test-key" {
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

// Helper function for tests
func stringPtr(s string) *string {
	return &s
}

// Benchmark tests
func BenchmarkGetMetadata(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := Response{
			Token:       "bench-token",
			PublicKey:   stringPtr("bench-key"),
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
		_, err := client.GetMetadata(context.Background(), "test-resource")
		if err != nil {
			b.Fatalf("Unexpected error: %v", err)
		}
	}
}

func BenchmarkGetMetadataCached(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := Response{
			Token:       "bench-token",
			PublicKey:   stringPtr("bench-key"),
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
	_, err := client.GetMetadata(context.Background(), "test-resource")
	if err != nil {
		b.Fatalf("Failed to prime cache: %v", err)
	}
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := client.GetMetadata(context.Background(), "test-resource")
		if err != nil {
			b.Fatalf("Unexpected error: %v", err)
		}
	}
}