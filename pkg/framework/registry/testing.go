package registry

import "testing"

// Reset clears all registered components. Call via t.Cleanup in tests.
// The testing.TB parameter is not used directly; it exists to prevent
// accidental calls from production code.
func Reset(tb testing.TB) {
	tb.Helper()
	mu.Lock()
	defer mu.Unlock()
	collectors = nil
	observers = nil
	operators = nil
}
