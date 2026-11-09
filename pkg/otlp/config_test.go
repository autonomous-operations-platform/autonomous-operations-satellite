package otlp_test

import (
	"context"
	"errors"
	"testing"

	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/framework/store"
	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/otlp"
)

func TestStart_InvalidConfig(t *testing.T) {
	logsOnly := []store.Signal{store.SignalLogs}
	ok := otlp.Endpoint{Name: "a", Addr: "127.0.0.1:0", Signals: logsOnly}
	with := func(f func(*otlp.Endpoint)) otlp.Endpoint {
		e := ok
		f(&e)
		return e
	}
	tests := []struct {
		name string
		cfg  otlp.Config
	}{
		{"nil store", otlp.Config{Endpoints: []otlp.Endpoint{ok}}},
		{"no endpoints", otlp.Config{Store: &fakeStore{}}},
		{"empty name", otlp.Config{Store: &fakeStore{}, Endpoints: []otlp.Endpoint{with(func(e *otlp.Endpoint) { e.Name = "" })}}},
		{"empty addr", otlp.Config{Store: &fakeStore{}, Endpoints: []otlp.Endpoint{with(func(e *otlp.Endpoint) { e.Addr = "" })}}},
		{"no signals", otlp.Config{Store: &fakeStore{}, Endpoints: []otlp.Endpoint{with(func(e *otlp.Endpoint) { e.Signals = nil })}}},
		{"unknown signal", otlp.Config{Store: &fakeStore{}, Endpoints: []otlp.Endpoint{with(func(e *otlp.Endpoint) { e.Signals = []store.Signal{"profiles"} })}}},
		{"duplicate signal", otlp.Config{Store: &fakeStore{}, Endpoints: []otlp.Endpoint{with(func(e *otlp.Endpoint) {
			e.Signals = []store.Signal{store.SignalLogs, store.SignalLogs}
		})}}},
		{"base path without slash", otlp.Config{Store: &fakeStore{}, Endpoints: []otlp.Endpoint{with(func(e *otlp.Endpoint) { e.BasePath = "v1" })}}},
		{"base path trailing slash", otlp.Config{Store: &fakeStore{}, Endpoints: []otlp.Endpoint{with(func(e *otlp.Endpoint) { e.BasePath = "/v1/" })}}},
		{"base path root", otlp.Config{Store: &fakeStore{}, Endpoints: []otlp.Endpoint{with(func(e *otlp.Endpoint) { e.BasePath = "/" })}}},
		{"base path unclean", otlp.Config{Store: &fakeStore{}, Endpoints: []otlp.Endpoint{with(func(e *otlp.Endpoint) { e.BasePath = "/a//b" })}}},
		{"base path pattern chars", otlp.Config{Store: &fakeStore{}, Endpoints: []otlp.Endpoint{with(func(e *otlp.Endpoint) { e.BasePath = "/{x}" })}}},
		{"negative body limit", otlp.Config{Store: &fakeStore{}, Endpoints: []otlp.Endpoint{with(func(e *otlp.Endpoint) { e.MaxBodyBytes = -1 })}}},
		{"duplicate name", otlp.Config{Store: &fakeStore{}, Endpoints: []otlp.Endpoint{ok, with(func(e *otlp.Endpoint) { e.BasePath = "/other" })}}},
		{"path collision on shared addr", otlp.Config{Store: &fakeStore{}, Endpoints: []otlp.Endpoint{
			{Name: "a", Addr: ":4318", Signals: logsOnly},
			{Name: "b", Addr: ":4318", Signals: logsOnly},
		}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, err := otlp.Start(t.Context(), tt.cfg)
			if !errors.Is(err, otlp.ErrInvalidConfig) {
				t.Fatalf("want ErrInvalidConfig, got %v", err)
			}
			if srv != nil {
				t.Error("want nil server on config error")
			}
		})
	}
}

func TestStart_SameSignalDifferentAddrIsValid(t *testing.T) {
	logsOnly := []store.Signal{store.SignalLogs}
	ctx, cancel := context.WithCancel(t.Context())
	srv, err := otlp.Start(ctx, otlp.Config{Store: &fakeStore{}, Endpoints: []otlp.Endpoint{
		{Name: "a", Addr: "127.0.0.1:0", Signals: logsOnly},
		{Name: "b", Addr: "127.0.0.2:0", Signals: logsOnly},
	}})
	if err != nil {
		t.Skipf("second loopback address unavailable: %v", err)
	}
	cancel()
	if err := srv.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestNewHandler_InvalidConfig(t *testing.T) {
	logsOnly := []store.Signal{store.SignalLogs}
	if _, err := otlp.NewHandler(otlp.Endpoint{Name: "a", Signals: logsOnly}, nil, nil); !errors.Is(err, otlp.ErrInvalidConfig) {
		t.Errorf("nil store: want ErrInvalidConfig, got %v", err)
	}
	if _, err := otlp.NewHandler(otlp.Endpoint{Name: "a"}, &fakeStore{}, nil); !errors.Is(err, otlp.ErrInvalidConfig) {
		t.Errorf("no signals: want ErrInvalidConfig, got %v", err)
	}
}

func TestNewHandler_IgnoresAddr(t *testing.T) {
	logsOnly := []store.Signal{store.SignalLogs}
	if _, err := otlp.NewHandler(otlp.Endpoint{Name: "a", Signals: logsOnly}, &fakeStore{}, nil); err != nil {
		t.Fatalf("NewHandler without Addr: %v", err)
	}
}
