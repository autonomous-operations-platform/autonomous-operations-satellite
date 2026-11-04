package registry_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/framework/collector"
	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/framework/observer"
	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/framework/operator"
	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/framework/registry"
	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/framework/store"
)

// Minimal fakes — satisfy the interfaces with the least possible code.

type fakeCollector struct{ name string }

func (f *fakeCollector) Name() string                                              { return f.name }
func (f *fakeCollector) Setup(context.Context, *slog.Logger, store.CollectorStore) {}

var _ collector.Collector = (*fakeCollector)(nil)

type fakeObserver struct{ name string }

func (f *fakeObserver) Name() string                  { return f.name }
func (f *fakeObserver) Filter() store.TelemetryFilter { return store.TelemetryFilter{} }
func (f *fakeObserver) Detect(context.Context, *slog.Logger, store.TelemetryRecord, store.ObserverStore) error {
	return nil
}

var _ observer.Observer = (*fakeObserver)(nil)

type fakeOperator struct{ name string }

func (f *fakeOperator) Name() string                { return f.name }
func (f *fakeOperator) Filter() store.InsightFilter { return store.InsightFilter{} }
func (f *fakeOperator) Execute(context.Context, *slog.Logger, store.Insight, store.OperatorStore) error {
	return nil
}

var _ operator.Operator = (*fakeOperator)(nil)

// ---------------------------------------------------------------------------
// Collectors
// ---------------------------------------------------------------------------

func TestRegisterCollector_Appended(t *testing.T) {
	t.Cleanup(func() { registry.Reset(t) })

	registry.RegisterCollector(&fakeCollector{"c1"})
	registry.RegisterCollector(&fakeCollector{"c2"})

	got := registry.Collectors()
	if len(got) != 2 {
		t.Fatalf("want 2 collectors, got %d", len(got))
	}
	if got[0].Name() != "c1" || got[1].Name() != "c2" {
		t.Errorf("order wrong: got %s, %s", got[0].Name(), got[1].Name())
	}
}

func TestCollectors_ReturnsCopy(t *testing.T) {
	t.Cleanup(func() { registry.Reset(t) })

	registry.RegisterCollector(&fakeCollector{"c1"})
	first := registry.Collectors()
	first[0] = &fakeCollector{"mutated"}

	second := registry.Collectors()
	if second[0].Name() != "c1" {
		t.Errorf("registry was mutated via returned slice: got %s", second[0].Name())
	}
}

// ---------------------------------------------------------------------------
// Observers
// ---------------------------------------------------------------------------

func TestRegisterObserver_Appended(t *testing.T) {
	t.Cleanup(func() { registry.Reset(t) })

	registry.RegisterObserver(&fakeObserver{"o1"})
	registry.RegisterObserver(&fakeObserver{"o2"})

	got := registry.Observers()
	if len(got) != 2 {
		t.Fatalf("want 2 observers, got %d", len(got))
	}
	if got[0].Name() != "o1" || got[1].Name() != "o2" {
		t.Errorf("order wrong: got %s, %s", got[0].Name(), got[1].Name())
	}
}

// ---------------------------------------------------------------------------
// Operators
// ---------------------------------------------------------------------------

func TestRegisterOperator_Appended(t *testing.T) {
	t.Cleanup(func() { registry.Reset(t) })

	registry.RegisterOperator(&fakeOperator{"op1"})
	registry.RegisterOperator(&fakeOperator{"op2"})

	got := registry.Operators()
	if len(got) != 2 {
		t.Fatalf("want 2 operators, got %d", len(got))
	}
	if got[0].Name() != "op1" || got[1].Name() != "op2" {
		t.Errorf("order wrong: got %s, %s", got[0].Name(), got[1].Name())
	}
}

// ---------------------------------------------------------------------------
// Concurrency — race detector catches issues only if goroutines actually run
// ---------------------------------------------------------------------------

func TestRegistry_Concurrent(t *testing.T) {
	t.Cleanup(func() { registry.Reset(t) })

	var wg sync.WaitGroup
	for range 10 {
		wg.Add(3)
		go func() {
			defer wg.Done()
			registry.RegisterCollector(&fakeCollector{"c"})
		}()
		go func() {
			defer wg.Done()
			registry.RegisterObserver(&fakeObserver{"o"})
		}()
		go func() {
			defer wg.Done()
			_ = registry.Collectors()
			_ = registry.Observers()
			_ = registry.Operators()
		}()
	}
	wg.Wait()
}
