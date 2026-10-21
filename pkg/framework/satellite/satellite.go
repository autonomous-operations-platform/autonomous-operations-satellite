package satellite

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/autonomous-operations/aos-framework/pkg/framework/internal/collectorstorewrapper"
	"github.com/autonomous-operations/aos-framework/pkg/framework/internal/observerrunner"
	"github.com/autonomous-operations/aos-framework/pkg/framework/internal/operatorrunner"
	"github.com/autonomous-operations/aos-framework/pkg/framework/registry"
	"github.com/autonomous-operations/aos-framework/pkg/framework/store"
)

var (
	ErrNoCollectors              = errors.New("satellite: at least one collector must be registered")
	ErrOperatorsWithoutObservers = errors.New("satellite: operators require at least one observer")
	ErrEmptyName                 = errors.New("satellite: component name must not be empty")
	ErrDuplicateName             = errors.New("satellite: duplicate component name")
)

type Config struct {
	Store  store.Store
	Logger *slog.Logger
}

func Run(ctx context.Context, cfg Config) error {
	collectors := registry.Collectors()
	observers := registry.Observers()
	operators := registry.Operators()

	if len(collectors) == 0 {
		return ErrNoCollectors
	}
	if len(operators) > 0 && len(observers) == 0 {
		return ErrOperatorsWithoutObservers
	}
	if err := validateNames(collectors); err != nil {
		return err
	}
	if err := validateNames(observers); err != nil {
		return err
	}
	if err := validateNames(operators); err != nil {
		return err
	}

	if cfg.Logger != nil {
		slog.SetDefault(cfg.Logger)
	}

	st := cfg.Store
	if st == nil {
		return errors.New("satellite: Config.Store is required")
	}

	satLog := slog.Default().With("component", "satellite")

	// start operators
	opRunners := make([]*operatorrunner.Runner, len(operators))
	for i, op := range operators {
		opLog := slog.Default().With("component", "operator", "name", op.Name())
		opLog.Info("starting")
		runner := operatorrunner.New(st, op)
		opRunners[i] = runner
		go func(r *operatorrunner.Runner, log *slog.Logger) {
			if err := r.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("operator exited", "err", err)
			}
		}(runner, opLog)
	}
	// wait for all operators to be ready (= have subscribed) before starting observers
	for _, r := range opRunners {
		if err := waitReady(ctx, r.Ready()); err != nil {
			return err
		}
	}

	// start observers
	obsRunners := make([]*observerrunner.Runner, len(observers))
	for i, obs := range observers {
		obsLog := slog.Default().With("component", "observer", "name", obs.Name())
		obsLog.Info("starting")
		runner := observerrunner.New(st, obs)
		obsRunners[i] = runner
		go func(r *observerrunner.Runner, log *slog.Logger) {
			if err := r.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("observer exited", "err", err)
			}
		}(runner, obsLog)
	}
	// wait for all observers to be ready (= have subscribed) before setting up collectors
	for _, r := range obsRunners {
		if err := waitReady(ctx, r.Ready()); err != nil {
			return err
		}
	}

	// set up collectors
	for _, c := range collectors {
		clog := slog.Default().With("component", "collector", "name", c.Name())
		cst := collectorstorewrapper.New(st, clog)
		clog.Info("setting up collector")
		c.Setup(ctx, clog, cst)
	}

	satLog.Info("running",
		"collectors", len(collectors),
		"observers", len(observers),
		"operators", len(operators),
	)
	<-ctx.Done()
	satLog.Info("shutting down")
	return nil
}

func waitReady(ctx context.Context, ch <-chan struct{}) error {
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func validateNames[T interface{ Name() string }](components []T) error {
	seen := make(map[string]struct{}, len(components))
	for _, c := range components {
		name := c.Name()
		if name == "" {
			return ErrEmptyName
		}
		if _, dup := seen[name]; dup {
			return fmt.Errorf("%q: %w", name, ErrDuplicateName)
		}
		seen[name] = struct{}{}
	}
	return nil
}
