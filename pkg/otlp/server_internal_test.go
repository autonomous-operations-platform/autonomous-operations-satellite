package otlp

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/framework/store"
)

type nopStore struct{}

func (nopStore) AddTelemetry(context.Context, []store.NewTelemetryRecord) ([]store.RecordID, error) {
	return nil, nil
}

// A listener that fails at runtime must stop the other listeners of the same
// Start call, and Wait must report the failure.
func TestServer_RuntimeListenerFailure(t *testing.T) {
	logs := []store.Signal{store.SignalLogs}
	srv, err := Start(t.Context(), Config{Store: nopStore{}, Endpoints: []Endpoint{
		{Name: "a", Addr: "127.0.0.1:0", Signals: logs},
		{Name: "b", Addr: "localhost:0", Signals: logs},
	}})
	if err != nil {
		t.Fatal(err)
	}
	other := srv.Addr("b").String()
	_ = srv.listeners[0].Close() // simulate an accept failure on listener a

	done := make(chan error, 1)
	go func() { done <- srv.Wait() }()
	select {
	case waitErr := <-done:
		if waitErr == nil {
			t.Fatal("Wait = nil, want the serve error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after a listener failure")
	}
	ln, err := net.Listen("tcp", other)
	if err != nil {
		t.Fatalf("listener b still open after a's failure: %v", err)
	}
	_ = ln.Close()
}
