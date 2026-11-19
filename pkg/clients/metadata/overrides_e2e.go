//go:build e2e

package metadata

import (
	"fmt"
	"os"
	"time"
)

// The end-to-end tests build the agent with the e2e tag, which compiles this
// file in, so they need not wait 10 s between token attempts. Release builds
// don't compile it.
func init() {
	if value := os.Getenv("SC_TEST_TOKEN_RETRY_DELAY"); value != "" {
		delay, err := time.ParseDuration(value)
		if err != nil {
			panic(fmt.Sprintf("SC_TEST_TOKEN_RETRY_DELAY=%q: %v", value, err))
		}
		retryDelay = delay
	}
}
