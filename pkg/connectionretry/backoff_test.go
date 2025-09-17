package retry

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestDefault(t *testing.T) {
	cfg := Default()
	
	if cfg.InitialDelay != DefaultInitialDelay {
		t.Errorf("Expected initial delay %v, got %v", DefaultInitialDelay, cfg.InitialDelay)
	}
	
	if cfg.MaxDelay != DefaultMaxDelay {
		t.Errorf("Expected max delay %v, got %v", DefaultMaxDelay, cfg.MaxDelay)
	}
	
	if cfg.Jitter != DefaultJitter {
		t.Errorf("Expected jitter %v, got %v", DefaultJitter, cfg.Jitter)
	}
	
	if cfg.MaxAttempts != 0 {
		t.Errorf("Expected max attempts 0 (infinite), got %d", cfg.MaxAttempts)
	}
}

func TestWithMaxAttempts(t *testing.T) {
	cfg := Default().WithMaxAttempts(5)
	
	if cfg.MaxAttempts != 5 {
		t.Errorf("Expected max attempts 5, got %d", cfg.MaxAttempts)
	}
}

func TestWithDelays(t *testing.T) {
	initial := 2 * time.Second
	max := 10 * time.Minute
	cfg := Default().WithDelays(initial, max)
	
	if cfg.InitialDelay != initial {
		t.Errorf("Expected initial delay %v, got %v", initial, cfg.InitialDelay)
	}
	
	if cfg.MaxDelay != max {
		t.Errorf("Expected max delay %v, got %v", max, cfg.MaxDelay)
	}
}

func TestWithJitter(t *testing.T) {
	jitter := 0.2
	cfg := Default().WithJitter(jitter)
	
	if cfg.Jitter != jitter {
		t.Errorf("Expected jitter %v, got %v", jitter, cfg.Jitter)
	}
}

func TestExecuteSuccess(t *testing.T) {
	logger := zap.NewNop()
	ctx := context.Background()
	
	callCount := 0
	operation := func(ctx context.Context) (string, error) {
		callCount++
		return "success", nil
	}
	
	cfg := Default().WithMaxAttempts(3)
	result, err := Execute(ctx, logger, "test-op", operation, cfg)
	
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	
	if result != "success" {
		t.Errorf("Expected result 'success', got '%s'", result)
	}
	
	if callCount != 1 {
		t.Errorf("Expected 1 call, got %d", callCount)
	}
}

func TestExecuteRetrySuccess(t *testing.T) {
	logger := zap.NewNop()
	ctx := context.Background()
	
	callCount := 0
	operation := func(ctx context.Context) (string, error) {
		callCount++
		if callCount < 3 {
			return "", errors.New("temporary failure")
		}
		return "success", nil
	}
	
	cfg := Default().WithMaxAttempts(5).WithDelays(10*time.Millisecond, 100*time.Millisecond)
	
	start := time.Now()
	result, err := Execute(ctx, logger, "test-op", operation, cfg)
	duration := time.Since(start)
	
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	
	if result != "success" {
		t.Errorf("Expected result 'success', got '%s'", result)
	}
	
	if callCount != 3 {
		t.Errorf("Expected 3 calls, got %d", callCount)
	}
	
	// Should have taken at least some time due to delays
	if duration < 10*time.Millisecond {
		t.Errorf("Expected some delay due to retries, but operation completed too quickly: %v", duration)
	}
}

func TestExecuteMaxAttemptsExceeded(t *testing.T) {
	logger := zap.NewNop()
	ctx := context.Background()
	
	callCount := 0
	operation := func(ctx context.Context) (string, error) {
		callCount++
		return "", errors.New("persistent failure")
	}
	
	cfg := Default().WithMaxAttempts(3).WithDelays(1*time.Millisecond, 10*time.Millisecond)
	result, err := Execute(ctx, logger, "test-op", operation, cfg)
	
	if err != ErrMaxRetriesExceeded {
		t.Errorf("Expected ErrMaxRetriesExceeded, got %v", err)
	}
	
	if result != "" {
		t.Errorf("Expected empty result on failure, got '%s'", result)
	}
	
	if callCount != 3 {
		t.Errorf("Expected 3 calls, got %d", callCount)
	}
}

func TestExecuteContextCancellation(t *testing.T) {
	logger := zap.NewNop()
	ctx, cancel := context.WithCancel(context.Background())
	
	callCount := 0
	operation := func(ctx context.Context) (string, error) {
		callCount++
		if callCount == 2 {
			cancel() // Cancel context on second call
		}
		return "", errors.New("failure")
	}
	
	cfg := Default().WithMaxAttempts(5).WithDelays(10*time.Millisecond, 100*time.Millisecond)
	result, err := Execute(ctx, logger, "test-op", operation, cfg)
	
	if err != context.Canceled {
		t.Errorf("Expected context.Canceled, got %v", err)
	}
	
	if result != "" {
		t.Errorf("Expected empty result on cancellation, got '%s'", result)
	}
	
	// Should have been called at least twice (once to fail, once to cancel)
	if callCount < 2 {
		t.Errorf("Expected at least 2 calls, got %d", callCount)
	}
}

func TestExecuteWithTimeout(t *testing.T) {
	logger := zap.NewNop()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	
	operation := func(ctx context.Context) (string, error) {
		return "", errors.New("failure")
	}
	
	cfg := Default().WithMaxAttempts(10).WithDelays(20*time.Millisecond, 100*time.Millisecond)
	
	start := time.Now()
	result, err := Execute(ctx, logger, "test-op", operation, cfg)
	duration := time.Since(start)
	
	if err != context.DeadlineExceeded {
		t.Errorf("Expected context.DeadlineExceeded, got %v", err)
	}
	
	if result != "" {
		t.Errorf("Expected empty result on timeout, got '%s'", result)
	}
	
	// Should have timed out around 50ms
	if duration > 100*time.Millisecond {
		t.Errorf("Operation took too long: %v", duration)
	}
}

func TestExecuteInfiniteRetries(t *testing.T) {
	logger := zap.NewNop()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	
	callCount := 0
	operation := func(ctx context.Context) (string, error) {
		callCount++
		return "", errors.New("failure")
	}
	
	cfg := Default().WithDelays(1*time.Millisecond, 10*time.Millisecond) // MaxAttempts = 0 (infinite)
	result, err := Execute(ctx, logger, "test-op", operation, cfg)
	
	if err != context.DeadlineExceeded {
		t.Errorf("Expected context.DeadlineExceeded, got %v", err)
	}
	
	if result != "" {
		t.Errorf("Expected empty result on timeout, got '%s'", result)
	}
	
	// Should have made multiple attempts
	if callCount < 5 {
		t.Errorf("Expected multiple calls with infinite retries, got %d", callCount)
	}
}

func TestNextDelay(t *testing.T) {
	tests := []struct {
		name         string
		current      time.Duration
		max          time.Duration
		jitterFactor float64
		expectMin    time.Duration
		expectMax    time.Duration
	}{
		{
			name:         "simple exponential backoff",
			current:      1 * time.Second,
			max:          10 * time.Second,
			jitterFactor: 0,
			expectMin:    2 * time.Second,
			expectMax:    2 * time.Second,
		},
		{
			name:         "hit max delay",
			current:      6 * time.Second,
			max:          10 * time.Second,
			jitterFactor: 0,
			expectMin:    10 * time.Second,
			expectMax:    10 * time.Second,
		},
		{
			name:         "with jitter",
			current:      1 * time.Second,
			max:          10 * time.Second,
			jitterFactor: 0.1,
			expectMin:    1800 * time.Millisecond, // 2s * 0.9
			expectMax:    2200 * time.Millisecond, // 2s * 1.1
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Run multiple times to test jitter variability
			for i := 0; i < 10; i++ {
				result := nextDelay(tt.current, tt.max, tt.jitterFactor)
				
				if result < tt.expectMin {
					t.Errorf("Result %v is less than expected minimum %v", result, tt.expectMin)
				}
				
				if result > tt.expectMax {
					t.Errorf("Result %v is greater than expected maximum %v", result, tt.expectMax)
				}
			}
		})
	}
}

func TestSimple(t *testing.T) {
	logger := zap.NewNop()
	ctx := context.Background()
	
	operation := func(ctx context.Context) (int, error) {
		return 42, nil
	}
	
	result, err := Simple(ctx, logger, "simple-test", operation)
	
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	
	if result != 42 {
		t.Errorf("Expected result 42, got %d", result)
	}
}

func TestErrMaxRetriesExceeded(t *testing.T) {
	if ErrMaxRetriesExceeded.Error() != "max retries exceeded" {
		t.Errorf("Unexpected error message: %s", ErrMaxRetriesExceeded.Error())
	}
}

// Benchmark tests
func BenchmarkExecuteSuccess(b *testing.B) {
	logger := zap.NewNop()
	ctx := context.Background()
	
	operation := func(ctx context.Context) (string, error) {
		return "success", nil
	}
	
	cfg := Default()
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := Execute(ctx, logger, "bench-op", operation, cfg)
		if err != nil {
			b.Fatalf("Unexpected error: %v", err)
		}
	}
}

func BenchmarkNextDelay(b *testing.B) {
	current := 1 * time.Second
	max := 5 * time.Minute
	jitter := 0.1
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		nextDelay(current, max, jitter)
	}
}