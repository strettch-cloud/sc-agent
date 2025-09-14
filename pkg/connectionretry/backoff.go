package retry

import (
	"context"
	"errors"
	"math/rand"
	"time"

	"go.uber.org/zap"
)

const (
	DefaultInitialDelay = time.Second
	DefaultMaxDelay     = 5 * time.Minute
	DefaultJitter       = 0.1
)

var ErrMaxRetriesExceeded = errors.New("max retries exceeded")

// Operation represents a function that can be retried
type Operation[T any] func(ctx context.Context) (T, error)

// Config holds retry configuration with exponential backoff
type Config struct {
	InitialDelay time.Duration
	MaxDelay     time.Duration
	Jitter       float64
	MaxAttempts  int // 0 means infinite
}

// Default returns a sensible default configuration
func Default() *Config {
	return &Config{
		InitialDelay: DefaultInitialDelay,
		MaxDelay:     DefaultMaxDelay,
		Jitter:       DefaultJitter,
		MaxAttempts:  0, // Infinite by default
	}
}

// WithMaxAttempts sets the maximum number of attempts (including first try)
func (c *Config) WithMaxAttempts(max int) *Config {
	c.MaxAttempts = max
	return c
}

// WithDelays sets both initial and max delay
func (c *Config) WithDelays(initial, max time.Duration) *Config {
	c.InitialDelay = initial
	c.MaxDelay = max
	return c
}

// WithJitter sets the jitter factor (0-1 range recommended)
func (c *Config) WithJitter(jitter float64) *Config {
	c.Jitter = jitter
	return c
}

// Execute runs the operation with exponential backoff retry logic
func Execute[T any](ctx context.Context, logger *zap.Logger, name string, op Operation[T], cfg *Config) (T, error) {
	if cfg == nil {
		cfg = Default()
	}
	
	var zero T
	delay := cfg.InitialDelay
	
	for attempt := 1; ; attempt++ {
		// Check context cancellation
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		
		// Check max attempts
		if cfg.MaxAttempts > 0 && attempt > cfg.MaxAttempts {
			return zero, ErrMaxRetriesExceeded
		}
		
		// Execute operation
		result, err := op(ctx)
		if err == nil {
			if attempt > 1 {
				logger.Info("Operation succeeded after retry",
					zap.String("operation", name),
					zap.Int("attempts", attempt))
			}
			return result, nil
		}
		
		// Last attempt failed
		if cfg.MaxAttempts > 0 && attempt == cfg.MaxAttempts {
			logger.Error("Operation failed after all attempts",
				zap.String("operation", name),
				zap.Error(err),
				zap.Int("attempts", attempt))
			return zero, ErrMaxRetriesExceeded
		}
		
		// Log retry
		fields := []zap.Field{
			zap.String("operation", name),
			zap.Error(err),
			zap.Int("attempt", attempt),
			zap.Duration("retry_in", delay),
		}
		if cfg.MaxAttempts > 0 {
			fields = append(fields, zap.Int("max_attempts", cfg.MaxAttempts))
		}
		logger.Warn("AUTOUPDATE: Retrying operation after failure", fields...)
		
		// Wait before retry
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return zero, ctx.Err()
		}
		
		// Calculate next delay
		delay = nextDelay(delay, cfg.MaxDelay, cfg.Jitter)
	}
}

// nextDelay calculates the next backoff duration with jitter
func nextDelay(current, max time.Duration, jitterFactor float64) time.Duration {
	// Double the delay (exponential backoff)
	next := current * 2
	if next > max {
		next = max
	}
	
	// Add jitter: random value in range [1-jitter, 1+jitter]
	if jitterFactor > 0 {
		multiplier := 1 + (rand.Float64()*2-1)*jitterFactor
		next = time.Duration(float64(next) * multiplier)
		
		// Ensure we stay within bounds
		if next < time.Second {
			next = time.Second
		}
		if next > max {
			next = max
		}
	}
	
	return next
}

// Simple is a convenience function for basic retry with defaults
func Simple[T any](ctx context.Context, logger *zap.Logger, name string, op Operation[T]) (T, error) {
	return Execute(ctx, logger, name, op, Default())
}