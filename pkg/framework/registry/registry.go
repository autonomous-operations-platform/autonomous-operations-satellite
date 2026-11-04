package registry

import (
	"sync"

	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/framework/collector"
	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/framework/observer"
	"github.com/autonomous-operations-platform/autonomous-operations-satellite/pkg/framework/operator"
)

var (
	//nolint:gochecknoglobals // globals are acceptable since this is a global framework
	mu sync.RWMutex
	//nolint:gochecknoglobals // globals are acceptable since this is a global framework
	collectors []collector.Collector
	//nolint:gochecknoglobals // globals are acceptable since this is a global framework
	observers []observer.Observer
	//nolint:gochecknoglobals // globals are acceptable since this is a global framework
	operators []operator.Operator
)

func RegisterCollector(c collector.Collector) {
	mu.Lock()
	defer mu.Unlock()
	collectors = append(collectors, c)
}

func RegisterObserver(o observer.Observer) {
	mu.Lock()
	defer mu.Unlock()
	observers = append(observers, o)
}

func RegisterOperator(o operator.Operator) {
	mu.Lock()
	defer mu.Unlock()
	operators = append(operators, o)
}

func Collectors() []collector.Collector {
	mu.RLock()
	defer mu.RUnlock()
	return append([]collector.Collector(nil), collectors...)
}

func Observers() []observer.Observer {
	mu.RLock()
	defer mu.RUnlock()
	return append([]observer.Observer(nil), observers...)
}

func Operators() []operator.Operator {
	mu.RLock()
	defer mu.RUnlock()
	return append([]operator.Operator(nil), operators...)
}
