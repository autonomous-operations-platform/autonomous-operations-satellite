package observer

import (
	"context"
	"log/slog"

	"github.tools.sap/autonomous-operations/aos-framework/pkg/framework/store"
)

type Observer interface {
	Name() string
	Filter() store.TelemetryFilter
	// The logger is pre-scoped with component=observer and name=Name(); use it
	// in preference to slog.Default().
	Detect(ctx context.Context, logger *slog.Logger, trigger store.TelemetryRecord, s store.ObserverStore) error
}
