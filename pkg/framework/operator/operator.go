package operator

import (
	"context"
	"log/slog"

	"github.com/autonomous-operations/aos-framework/pkg/framework/store"
)

type Operator interface {
	Name() string
	Filter() store.InsightFilter
	// The logger is pre-scoped with component=operator and name=Name(); use it
	// in preference to slog.Default().
	Execute(ctx context.Context, logger *slog.Logger, insight store.Insight, s store.OperatorStore) error
}
