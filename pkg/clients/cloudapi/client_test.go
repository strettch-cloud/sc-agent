package cloudapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/strettch/strettch-cloud/sc-console-agent/pkg/clients/metadata"
	"go.uber.org/zap"
)

func TestNewClient(t *testing.T) {
	// Create a simple metadata client
	metaClient := metadata.NewClient("http://example.com", 10*time.Second, zap.NewNop())
	resourceID := "test-resource-123"
	timeout := 30 * time.Second
	logger := zap.NewNop()

	client := NewClient(metaClient, resourceID, timeout, logger)

	if client == nil {
		t.Fatal("Expected non-nil client")
	}

	if client.resourceID != resourceID {
		t.Errorf("Expected resource ID %s, got %s", resourceID, client.resourceID)
	}

	if client.logger != logger {
		t.Error("Logger not properly set")
	}

	if client.httpClient.Timeout != timeout {
		t.Errorf("Expected timeout %v, got %v", timeout, client.httpClient.Timeout)
	}
}

func TestFetchConsoleKey(t *testing.T) {
	tests := []struct {
		name           string
		serverResponse func(w http.ResponseWriter, r *http.Request)
		authToken      string
		expectError    bool
		expectedKey    *ConsoleKey
	}{
		{
			name: "successful response",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				// Verify request headers
				authHeader := r.Header.Get(HeaderAuthorization)
				if authHeader != "Bearer test-token-123" {
					t.Errorf("Expected Authorization header 'Bearer test-token-123', got '%s'", authHeader)
				}

				if r.Header.Get(HeaderAccept) != AcceptJSON {
					t.Errorf("Expected Accept header %s, got %s", AcceptJSON, r.Header.Get(HeaderAccept))
				}

				// Verify URL path
				expectedPath := "/resource-manager/api/v1/console/public-key/agent"
				if r.URL.Path != expectedPath {
					t.Errorf("Expected path %s, got %s", expectedPath, r.URL.Path)
				}

				response := ConsoleKeyResponse{
					Data: ConsoleKey{
						PublicKey:   "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5...",
						Principal:   "root",
						RequesterID: "admin@example.com",
						CreatedAt:   "2023-01-01T10:00:00Z",
						ExpireAt:    "2023-01-02T10:00:00Z",
					},
				}

				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(response); err != nil {
					// Error encoding response in test
					_ = err // Suppress staticcheck SA9003
				}
			},
			authToken:   "test-token-123",
			expectError: false,
			expectedKey: &ConsoleKey{
				PublicKey:   "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5...",
				Principal:   "root",
				RequesterID: "admin@example.com",
				CreatedAt:   "2023-01-01T10:00:00Z",
				ExpireAt:    "2023-01-02T10:00:00Z",
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
			authToken:   "test-token-123",
			expectError: true,
		},
		{
			name: "empty public key",
			serverResponse: func(w http.ResponseWriter, r *http.Request) {
				response := ConsoleKeyResponse{
					Data: ConsoleKey{
						PublicKey:   "",
						Principal:   "root",
						RequesterID: "admin@example.com",
						CreatedAt:   "2023-01-01T10:00:00Z",
						ExpireAt:    "2023-01-02T10:00:00Z",
					},
				}

				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(response); err != nil {
					// Error encoding response in test
					_ = err // Suppress staticcheck SA9003
				}
			},
			authToken:   "test-token-123",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(tt.serverResponse))
			defer server.Close()

			// Create metadata server that returns the API URL
			metaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				response := metadata.Response{
					Token:       "meta-token",
					CloudAPIUrl: server.URL,
				}
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(response); err != nil {
					// Error encoding response in test
					_ = err // Suppress staticcheck SA9003
				}
			}))
			defer metaServer.Close()

			metaClient := metadata.NewClient(metaServer.URL, 10*time.Second, zap.NewNop())
			client := NewClient(metaClient, "test-resource", 10*time.Second, zap.NewNop())

			key, err := client.FetchConsoleKey(context.Background(), tt.authToken)

			if tt.expectError {
				if err == nil {
					t.Error("Expected error but got none")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}

				if key == nil {
					t.Fatal("Expected key but got nil")
				}

				if key.PublicKey != tt.expectedKey.PublicKey {
					t.Errorf("Expected public key %s, got %s", tt.expectedKey.PublicKey, key.PublicKey)
				}

				if key.Principal != tt.expectedKey.Principal {
					t.Errorf("Expected principal %s, got %s", tt.expectedKey.Principal, key.Principal)
				}
			}
		})
	}
}

func TestFetchConsoleKeyWithRetry(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		response := ConsoleKeyResponse{
			Data: ConsoleKey{
				PublicKey:   "ssh-ed25519 SUCCESS_KEY",
				Principal:   "root",
				RequesterID: "retry@example.com",
				CreatedAt:   "2023-01-01T10:00:00Z",
				ExpireAt:    "2023-01-02T10:00:00Z",
			},
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			// Error encoding response in test
			_ = err // Suppress staticcheck SA9003
		}
	}))
	defer server.Close()

	metaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := metadata.Response{
			Token:       "meta-token",
			CloudAPIUrl: server.URL,
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			// Error encoding response in test
			_ = err // Suppress staticcheck SA9003
		}
	}))
	defer metaServer.Close()

	metaClient := metadata.NewClient(metaServer.URL, 1*time.Second, zap.NewNop())
	client := NewClient(metaClient, "test-resource", 1*time.Second, zap.NewNop())

	key, err := client.FetchConsoleKeyWithRetry(context.Background(), "test-token", 5)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if key.PublicKey != "ssh-ed25519 SUCCESS_KEY" {
		t.Errorf("Expected success key, got %s", key.PublicKey)
	}

	if attempts != 3 {
		t.Errorf("Expected 3 attempts, got %d", attempts)
	}
}

func TestContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate slow response
		time.Sleep(100 * time.Millisecond)
		response := ConsoleKeyResponse{
			Data: ConsoleKey{
				PublicKey: "test-key",
				Principal: "root",
			},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			// Error encoding response in test
			_ = err // Suppress staticcheck SA9003
		}
	}))
	defer server.Close()

	metaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := metadata.Response{
			Token:       "meta-token",
			CloudAPIUrl: server.URL,
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			// Error encoding response in test
			_ = err // Suppress staticcheck SA9003
		}
	}))
	defer metaServer.Close()

	metaClient := metadata.NewClient(metaServer.URL, 10*time.Second, zap.NewNop())
	client := NewClient(metaClient, "test-resource", 10*time.Second, zap.NewNop())

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := client.FetchConsoleKey(ctx, "test-token")
	if err == nil {
		t.Error("Expected timeout error")
	}

	if !strings.Contains(err.Error(), "context") {
		t.Errorf("Expected context-related error, got: %v", err)
	}
}

func TestClose(t *testing.T) {
	metaClient := metadata.NewClient("http://example.com", 10*time.Second, zap.NewNop())
	client := NewClient(metaClient, "test-resource", 10*time.Second, zap.NewNop())

	// This should not panic
	client.Close()
}

func TestConstants(t *testing.T) {
	if HeaderAccept != "Accept" {
		t.Errorf("Expected HeaderAccept to be 'Accept', got '%s'", HeaderAccept)
	}

	if HeaderAuthorization != "Authorization" {
		t.Errorf("Expected HeaderAuthorization to be 'Authorization', got '%s'", HeaderAuthorization)
	}

	if AcceptJSON != "application/json" {
		t.Errorf("Expected AcceptJSON to be 'application/json', got '%s'", AcceptJSON)
	}
}

func TestConsoleKeyStruct(t *testing.T) {
	key := ConsoleKey{
		PublicKey:   "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5...",
		Principal:   "root",
		RequesterID: "user@example.com",
		CreatedAt:   "2023-01-01T10:00:00Z",
		ExpireAt:    "2023-01-02T10:00:00Z",
	}

	if key.PublicKey == "" {
		t.Error("PublicKey should not be empty")
	}

	if key.Principal != "root" {
		t.Errorf("Expected principal 'root', got '%s'", key.Principal)
	}
}
