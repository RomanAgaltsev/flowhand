package obs

import (
	"context"
	"errors"
	"log/slog"

	"github.com/RomanAgaltsev/flowhand/internal/config"
)

type ShutdownFunc func(context.Context) error

func Init(ctx context.Context, cfg *config.Config) (ShutdownFunc, error) {
	logger := NewLogger(&cfg.Obs)
	slog.SetDefault(logger)

	var shutdownFuncs []func(context.Context) error
	var err error

	tp, err := NewTracerProvider(ctx, cfg)
	if err != nil {
		return nil, err
	}
	shutdownFuncs = append(shutdownFuncs, tp.Shutdown)

	mp, err := NewMeterProvider(ctx, cfg)
	if err != nil {
		return nil, err
	}
	shutdownFuncs = append(shutdownFuncs, mp.Shutdown)

	shutdownFunc := func(ctx context.Context) error {
		var err error
		for _, fn := range shutdownFuncs {
			err = errors.Join(err, fn(ctx))
		}
		shutdownFuncs = nil
		return err
	}

	return shutdownFunc, nil
}
