package collector

import (
	"context"
	"log/slog"

	"github.tools.sap/autonomous-operations/aos-framework/pkg/framework/store"
)

type Collector interface {
	Name() string
	// Setup starts the collector's ingestion mechanism and returns immediately.
	// Push-based collectors register HTTP handlers; pull-based collectors start
	// a scheduled goroutine. The collector owns its lifecycle until ctx is cancelled.
	// The logger is pre-scoped with component=collector and name=Name(); use it
	// in preference to slog.Default().
	Setup(ctx context.Context, logger *slog.Logger, s store.CollectorStore)
}
