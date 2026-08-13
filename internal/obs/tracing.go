package obs

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/trace"

	"github.com/RomanAgaltsev/flowhand/internal/config"
)

// NewTracerProvider builds an OTel tracer provider that exports spans over OTLP/gRPC.
func NewTracerProvider(ctx context.Context, cfg *config.Config) (*trace.TracerProvider, error) {
	traceExporter, err := otlptracegrpc.New(
		ctx,
		otlptracegrpc.WithEndpoint(cfg.Obs.OTLPEndpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}

	res, err := newResource(cfg)
	if err != nil {
		return nil, fmt.Errorf("merge resource: %w", err)
	}

	traceProvider := trace.NewTracerProvider(
		trace.WithBatcher(
			traceExporter,
			trace.WithBatchTimeout(time.Second),
		),
		trace.WithResource(res),
	)

	return traceProvider, nil
}
