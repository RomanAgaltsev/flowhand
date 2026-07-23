package obs

import (
	"context"
	"errors"
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/RomanAgaltsev/flowhand/internal/config"
)

// ShutdownFunc releases the resources wired up by Init.
type ShutdownFunc func(context.Context) error

// Init wires up logging, tracing and metrics, and returns the registry that
// /metrics must serve - the OTel metrics live there, not in the default one.
func Init(ctx context.Context, cfg *config.Config) (ShutdownFunc, *prometheus.Registry, error) {
	logger := NewLogger(&cfg.Obs)
	slog.SetDefault(logger)

	// Set first: OTel reports errors from the providers below through this.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		slog.Error("otel", "err", err)
	}))

	var shutdownFuncs []func(context.Context) error
	var err error

	tp, err := NewTracerProvider(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	shutdownFuncs = append(shutdownFuncs, tp.Shutdown)

	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	mp, err := NewMeterProvider(cfg, reg)
	if err != nil {
		return nil, nil, err
	}
	otel.SetMeterProvider(mp)
	shutdownFuncs = append(shutdownFuncs, mp.Shutdown)

	shutdownFunc := func(ctx context.Context) error {
		var err error
		for _, fn := range shutdownFuncs {
			err = errors.Join(err, fn(ctx))
		}
		shutdownFuncs = nil
		return err
	}

	return shutdownFunc, reg, nil
}
