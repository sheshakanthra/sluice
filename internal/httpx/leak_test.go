package httpx

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain fails the package if any test leaves a goroutine running (P7).
func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }
